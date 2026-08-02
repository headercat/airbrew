// Package agent — agent.go
//
// The agent runtime: build the message stream, call the LLM provider,
// forward SSE deltas, dispatch tool calls, and persist the final turn.
//
// One Run = one assistant response, possibly spanning several tool calls.
// The runtime owns the SSE channel and closes it when the assistant turn
// finishes (cleanly or on error).
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"github.com/headercat/airbrew/internal/ai/conv"
	"github.com/headercat/airbrew/internal/ai/provider"
)

// Event is one streamed frame the handler forwards to the SSE client.
type Event struct {
	Kind       EventKind       `json:"kind"`
	Content    string          `json:"content,omitempty"`
	ToolCallID string          `json:"tool_call_id,omitempty"`
	ToolName   string          `json:"tool_name,omitempty"`
	ToolArgs   string          `json:"tool_args,omitempty"`
	ToolResult string          `json:"tool_result,omitempty"`
	Usage      *provider.Usage `json:"usage,omitempty"`
	MessageID  string          `json:"message_id,omitempty"`
	Err        *ErrorBody      `json:"error,omitempty"`
}

// EventKind enumerates the SSE event types.
type EventKind string

const (
	EventDelta     EventKind = "delta"      // assistant content fragment
	EventToolStart EventKind = "tool_start" // tool call started
	EventTool      EventKind = "tool"       // tool call + result
	EventDone      EventKind = "done"       // turn finished
	EventError     EventKind = "error"      // fatal error
	EventMetadata  EventKind = "metadata"   // out-of-band info (model id, etc)
)

// ErrorBody is the wire shape for Event.Err.
type ErrorBody struct {
	Code        string `json:"code"`
	Description string `json:"description"`
}

// ErrRunInProgress indicates another assistant turn is already running
// for the same user conversation.
var ErrRunInProgress = errors.New("agent: conversation run already in progress")

// Spec is the per-conversation snapshot the runtime executes against.
// The handler builds it from the conversation row.
type Spec struct {
	Model       string
	System      string
	Tools       []string
	Temperature float32
	MaxTokens   int
	MaxTurns    int
}

// Runtime wires together everything Run needs.
type Runtime struct {
	conv       *conv.Service
	tools      *ToolRegistry
	runMu      sync.Mutex
	activeRuns map[string]struct{}
	// ResolveProvider returns an LLMClient for the active provider. It is
	// injected so the runtime does not import the provider repository
	// (and tests can substitute a fake).
	ResolveProvider func(ctx context.Context) (provider.LLMClient, error)
}

// New returns a Runtime. tools and ResolveProvider are required.
func New(c *conv.Service, tools *ToolRegistry, resolve func(ctx context.Context) (provider.LLMClient, error)) *Runtime {
	return &Runtime{conv: c, tools: tools, ResolveProvider: resolve, activeRuns: map[string]struct{}{}}
}

// AutoTitle runs a one-shot non-streaming turn that produces a short
// title from the user's first message. It is best-effort: any failure
// (no provider, upstream error, decode error) leaves the conversation
// title untouched so the SPA can fall back to "Untitled".
func (rt *Runtime) AutoTitle(ctx context.Context, userID, conversationID, userMessage string) (string, error) {
	if rt.ResolveProvider == nil {
		return "", errors.New("no provider resolver")
	}
	cli, err := rt.ResolveProvider(ctx)
	if err != nil {
		return "", err
	}
	prompt := "Generate a concise 3-6 word title for a conversation that " +
		"begins with the user's message below. Reply with the title only — " +
		"no quotes, no punctuation, no explanation.\n\nUser: " + userMessage
	deltas := cli.ChatStream(ctx, provider.Request{
		Model:     cli.Model(),
		Messages:  []provider.Message{{Role: provider.RoleUser, Content: prompt}},
		MaxTokens: 24,
	})
	var b strings.Builder
	for d := range deltas {
		switch d.Kind {
		case provider.DeltaContent:
			b.WriteString(d.Content)
		case provider.DeltaError:
			return "", d.Err
		case provider.DeltaDone:
			// continue to drain
		}
	}
	title := strings.TrimSpace(b.String())
	// Trim trailing period / quotes the model may add despite instructions.
	title = strings.Trim(title, "\"'“”.")
	if len(title) > conv.MaxTitleLen {
		title = title[:conv.MaxTitleLen]
	}
	if title == "" {
		return "", errors.New("empty title from provider")
	}
	if err := rt.conv.SetTitle(ctx, userID, conversationID, title); err != nil {
		return "", err
	}
	return title, nil
}

// RunInput is one chat request.
type RunInput struct {
	UserID         string
	ConversationID string
	UserMessage    string
	Spec           Spec
}

// Run executes one assistant turn and streams events. It is the only
// public entry point; the handler adapts the Event channel into SSE.
//
// Loop:
//  1. Append the user's message.
//  2. Build the provider message list (system + history).
//  3. Call the provider. Forward text deltas as EventDelta.
//  4. If the assistant emitted tool calls, dispatch each, emit EventTool,
//     append the tool message, and loop again (subject to MaxTurns).
//  5. On a turn with no tool calls, persist the assistant message and
//     emit EventDone.
func (rt *Runtime) Run(ctx context.Context, in RunInput) <-chan Event {
	out := make(chan Event, 32)
	go func() {
		defer close(out)
		if err := rt.run(ctx, in, out); err != nil {
			_ = sendEvent(ctx, out, Event{Kind: EventError, Err: toEventError(err)})
		}
	}()
	return out
}

func (rt *Runtime) run(ctx context.Context, in RunInput, out chan<- Event) error {
	unlock, ok := rt.tryLockRun(in.UserID, in.ConversationID)
	if !ok {
		return ErrRunInProgress
	}
	defer unlock()
	if rt.ResolveProvider == nil {
		return errors.New("agent: provider resolver not configured")
	}
	cli, err := rt.ResolveProvider(ctx)
	if err != nil {
		return fmt.Errorf("resolve provider: %w", err)
	}
	if !sendEvent(ctx, out, Event{Kind: EventMetadata, Content: cli.Model()}) {
		return ctx.Err()
	}

	// 1. Persist the user's prompt first so the conversation reflects the
	//    request even if the provider call never returns.
	if _, err := rt.conv.AppendUserMessage(ctx, in.UserID, in.ConversationID, in.UserMessage); err != nil {
		return fmt.Errorf("append user message: %w", err)
	}

	maxTurns := in.Spec.MaxTurns
	if maxTurns <= 0 {
		maxTurns = 6
	}
	allowedTools := toolAllowList(in.Spec.Tools)

	var lastUsage provider.Usage
	for turn := 0; turn < maxTurns; turn++ {
		// 2. Rebuild history each turn so the previous assistant + tool
		//    messages are visible. Snap the system prompt from the spec.
		//    Trim to MaxHistoryMessages to bound prompt cost on long
		//    conversations (docs/ai.md).
		hist, err := rt.conv.History(ctx, in.UserID, in.ConversationID)
		if err != nil {
			return fmt.Errorf("load history: %w", err)
		}
		hist = trimHistory(hist, MaxHistoryMessages)
		msgs := buildProviderMessages(in.Spec.System, hist)

		// 3. Stream the assistant response.
		acc := newAssistantAccumulator()
		req := provider.Request{
			Model: in.Spec.Model, Messages: msgs,
			Temperature: in.Spec.Temperature, MaxTokens: in.Spec.MaxTokens,
		}
		if len(in.Spec.Tools) > 0 {
			req.Tools = rt.tools.SchemasFor(in.Spec.Tools)
		}

		deltas := cli.ChatStream(ctx, req)
		streamDone := false
	accLoop:
		for d := range deltas {
			switch d.Kind {
			case provider.DeltaContent:
				acc.content.WriteString(d.Content)
				if !sendEvent(ctx, out, Event{Kind: EventDelta, Content: d.Content}) {
					return ctx.Err()
				}
			case provider.DeltaToolCallStart:
				acc.startTool(d.Index, d.ToolCallID, d.ToolName)
			case provider.DeltaToolCallArgs:
				acc.appendToolArgs(d.Index, d.Content)
			case provider.DeltaDone:
				streamDone = true
				if d.Usage != nil {
					lastUsage = *d.Usage
				}
				break accLoop
			case provider.DeltaError:
				if errors.Is(d.Err, context.Canceled) {
					return d.Err
				}
				return fmt.Errorf("provider stream: %w", d.Err)
			}
		}
		if !streamDone {
			return errors.New("provider stream closed without done")
		}

		// 4. No tool calls => terminal turn. Persist and emit done.
		if acc.toolCalls == nil || len(acc.toolCalls) == 0 {
			amsg, perr := rt.conv.AppendAssistantMessage(ctx, in.UserID, in.ConversationID,
				acc.content.String(), nil, lastUsage.PromptTokens, lastUsage.CompletionTokens)
			if perr != nil {
				return fmt.Errorf("append assistant message: %w", perr)
			}
			if !sendEvent(ctx, out, Event{
				Kind: EventDone, MessageID: amsg.ID,
				Usage: &provider.Usage{
					PromptTokens:     lastUsage.PromptTokens,
					CompletionTokens: lastUsage.CompletionTokens,
				},
			}) {
				return ctx.Err()
			}
			return nil
		}

		// 5. Dispatch each tool call, emit results, and append the tool
		//    messages so the next turn sees them.
		persistedToolCalls := acc.completedToolCalls(turn)
		// Persist the assistant turn that issued the tool calls first,
		// mirroring how the wire transcript will look to the model next turn.
		if _, err := rt.conv.AppendAssistantMessage(ctx, in.UserID, in.ConversationID,
			acc.content.String(), providerToolCalls(persistedToolCalls),
			lastUsage.PromptTokens, lastUsage.CompletionTokens,
		); err != nil {
			return fmt.Errorf("append assistant tool turn: %w", err)
		}

		for _, tc := range persistedToolCalls {
			if !sendEvent(ctx, out, Event{
				Kind: EventToolStart, ToolCallID: tc.id, ToolName: tc.name,
				ToolArgs: tc.args,
			}) {
				return ctx.Err()
			}
			result, _ := dispatch(ctx, rt.tools, allowedTools, tc.name, tc.args)
			result = truncateToolResult(result)
			if !sendEvent(ctx, out, Event{
				Kind: EventTool, ToolCallID: tc.id, ToolName: tc.name,
				ToolArgs: tc.args, ToolResult: result,
			}) {
				return ctx.Err()
			}
			if _, err := rt.conv.AppendToolMessage(ctx, in.UserID, in.ConversationID,
				tc.id, tc.name, result,
			); err != nil {
				return fmt.Errorf("append tool message: %w", err)
			}
		}
	}

	return fmt.Errorf("agent: hit max_turns (%d) without a terminal assistant turn", maxTurns)
}

func (rt *Runtime) tryLockRun(userID, conversationID string) (func(), bool) {
	key := userID + "\x00" + conversationID
	rt.runMu.Lock()
	defer rt.runMu.Unlock()
	if rt.activeRuns == nil {
		rt.activeRuns = map[string]struct{}{}
	}
	if _, exists := rt.activeRuns[key]; exists {
		return nil, false
	}
	rt.activeRuns[key] = struct{}{}
	return func() {
		rt.runMu.Lock()
		delete(rt.activeRuns, key)
		rt.runMu.Unlock()
	}, true
}

func sendEvent(ctx context.Context, out chan<- Event, ev Event) bool {
	select {
	case <-ctx.Done():
		return false
	case out <- ev:
		return true
	}
}

func toolAllowList(keys []string) map[string]struct{} {
	out := make(map[string]struct{}, len(keys))
	for _, k := range keys {
		k = strings.TrimSpace(k)
		if k != "" {
			out[k] = struct{}{}
		}
	}
	return out
}

func normalizedToolCallID(id string, turn, index int) string {
	id = strings.TrimSpace(id)
	if id != "" {
		return id
	}
	return fmt.Sprintf("call_%d_%d", turn, index)
}

func truncateToolResult(s string) string {
	if len(s) <= conv.MaxContentLen {
		return s
	}
	const suffix = "\n\n[tool result truncated]"
	limit := conv.MaxContentLen - len(suffix)
	if limit < 0 {
		limit = conv.MaxContentLen
	}
	return s[:limit] + suffix
}

// MaxHistoryMessages bounds the number of past turns replayed to the
// provider on each turn. Long conversations trim from the head so the
// most recent context (and any in-flight tool exchange) survive.
const MaxHistoryMessages = 50

// trimHistory returns the most recent n messages of hist, then repairs
// tool-call transcript boundaries. Providers expect assistant tool_calls
// to be followed by their matching tool result rows; partial exchanges
// are dropped instead of replayed.
func trimHistory(hist []provider.Message, n int) []provider.Message {
	if n > 0 && len(hist) > n {
		hist = hist[len(hist)-n:]
	}
	return repairToolTranscript(hist)
}

func repairToolTranscript(hist []provider.Message) []provider.Message {
	out := make([]provider.Message, 0, len(hist))
	for i := 0; i < len(hist); i++ {
		m := hist[i]
		if m.Role == provider.RoleTool {
			continue
		}
		if m.Role != provider.RoleAssistant || len(m.ToolCalls) == 0 {
			out = append(out, m)
			continue
		}
		if !hasCompleteToolResults(hist, i) {
			for i+1 < len(hist) && hist[i+1].Role == provider.RoleTool {
				i++
			}
			continue
		}
		out = append(out, m)
		for range m.ToolCalls {
			i++
			out = append(out, hist[i])
		}
	}
	return out
}

func hasCompleteToolResults(hist []provider.Message, assistantIndex int) bool {
	calls := hist[assistantIndex].ToolCalls
	if assistantIndex+len(calls) >= len(hist) {
		return false
	}
	for i, tc := range calls {
		next := hist[assistantIndex+1+i]
		if next.Role != provider.RoleTool || next.ToolCallID != tc.ID {
			return false
		}
	}
	return true
}

// buildProviderMessages prepends the system prompt to history. If the
// history already starts with a system message (e.g. an older snapshot),
// it is replaced rather than duplicated so the active Spec wins.
func buildProviderMessages(system string, hist []provider.Message) []provider.Message {
	system = strings.TrimSpace(system)
	out := make([]provider.Message, 0, len(hist)+1)
	if system != "" {
		out = append(out, provider.Message{Role: provider.RoleSystem, Content: system})
	}
	for _, m := range hist {
		if m.Role == provider.RoleSystem && system != "" {
			continue // drop stale system rows in favour of the spec
		}
		out = append(out, m)
	}
	return out
}

// assistantAccumulator merges streamed deltas into complete content + tool
// calls. OpenAI emits tool args as fragments; Anthropic emits the start
// event with id+name then args as input_json_delta fragments; Ollama
// streams the whole args blob at once. The accumulator copes with all
// three by indexing on the slot number.
type assistantAccumulator struct {
	content   strings.Builder
	toolCalls []*assistantToolCall
}

type assistantToolCall struct {
	id   string
	name string
	args strings.Builder
}

type completedToolCall struct {
	id   string
	name string
	args string
}

func newAssistantAccumulator() *assistantAccumulator {
	return &assistantAccumulator{}
}

func (a *assistantAccumulator) startTool(index int, id, name string) {
	for len(a.toolCalls) <= index {
		a.toolCalls = append(a.toolCalls, nil)
	}
	if a.toolCalls[index] == nil {
		a.toolCalls[index] = &assistantToolCall{}
	}
	if id != "" {
		a.toolCalls[index].id = id
	}
	if name != "" {
		a.toolCalls[index].name = name
	}
}

func (a *assistantAccumulator) appendToolArgs(index int, args string) {
	for len(a.toolCalls) <= index {
		a.toolCalls = append(a.toolCalls, nil)
	}
	if a.toolCalls[index] == nil {
		a.toolCalls[index] = &assistantToolCall{}
	}
	a.toolCalls[index].args.WriteString(args)
}

func (a *assistantAccumulator) completedToolCalls(turn int) []completedToolCall {
	out := make([]completedToolCall, 0, len(a.toolCalls))
	for i, tc := range a.toolCalls {
		if tc == nil {
			continue
		}
		out = append(out, completedToolCall{
			id: normalizedToolCallID(tc.id, turn, i), name: tc.name, args: tc.args.String(),
		})
	}
	return out
}

func providerToolCalls(calls []completedToolCall) []provider.ToolCall {
	out := make([]provider.ToolCall, 0, len(calls))
	for _, tc := range calls {
		out = append(out, provider.ToolCall{ID: tc.id, Name: tc.name, Args: tc.args})
	}
	return out
}

func toEventError(err error) *ErrorBody {
	if errors.Is(err, provider.ErrUpstream) {
		// upstream errors carry the response body for logging only; the
		// client just sees the status code so it cannot be used to
		// exfiltrate provider output (which may echo back part of a key).
		if u := provider.AsUpstream(err); u != nil {
			slog.Default().Warn("ai: provider upstream error",
				"status", u.Status(), "body", u.Body())
			return &ErrorBody{
				Code:        "provider_upstream",
				Description: fmt.Sprintf("provider returned HTTP %d", u.Status()),
			}
		}
		return &ErrorBody{Code: "provider_upstream", Description: "provider error"}
	}
	if errors.Is(err, context.Canceled) {
		return &ErrorBody{Code: "cancelled", Description: "request cancelled"}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return &ErrorBody{Code: "timeout", Description: "request timed out"}
	}
	if errors.Is(err, ErrRunInProgress) {
		return &ErrorBody{Code: "conversation_busy", Description: "conversation already has a running turn"}
	}
	// Surface only the top-level message; the wrapped chain may contain
	// internal paths or DB errors that should not reach the SPA.
	return &ErrorBody{Code: "internal_error", Description: "internal error"}
}

// upstreamStatus is a tiny helper so callers can read the HTTP status
// without importing provider. Used in tests.
func upstreamStatus(err error) (int, bool) {
	if u := provider.AsUpstream(err); u != nil {
		return u.Status(), true
	}
	return 0, false
}

// DecodeArgs is a helper for the SPA: it returns a pretty-printed version
// of the tool arguments JSON for display.
func DecodeArgs(raw string) string {
	var anyVal any
	if err := json.Unmarshal([]byte(raw), &anyVal); err != nil {
		return raw
	}
	b, err := json.MarshalIndent(anyVal, "", "  ")
	if err != nil {
		return raw
	}
	return string(b)
}
