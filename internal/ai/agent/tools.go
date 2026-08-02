// Package agent — tools.go
//
// Tool framework for the agent runtime. A Tool is a function the model
// may invoke; its Schema is sent to the provider so the model knows what
// arguments to produce. Dispatch lives in agent.Run.
//
// Tools are designed to be cheap to register, simple to author, and
// bounded in execution: every Call is wrapped in a per-tool timeout.
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/headercat/airbrew/internal/ai/provider"
)

// Tool is one function the agent can invoke.
type Tool interface {
	Schema() provider.ToolSchema
	// Call executes the tool. args is the raw JSON arguments string from
	// the model; the implementation may unmarshal it into a typed struct.
	// The returned string becomes the role=tool message content.
	Call(ctx context.Context, args string) (string, error)
}

// ToolRegistry tracks tools by key. Registration can happen at init
// (built-ins) or at runtime (per-module integrations like vault.search).
type ToolRegistry struct {
	mu    sync.RWMutex
	tools map[string]Tool
}

// NewToolRegistry returns an empty registry.
func NewToolRegistry() *ToolRegistry {
	return &ToolRegistry{tools: map[string]Tool{}}
}

// Register adds a tool under key. Panics on duplicates so a startup bug
// fails loudly.
func (r *ToolRegistry) Register(key string, t Tool) {
	key = strings.TrimSpace(key)
	if key == "" {
		panic("agent: empty tool key")
	}
	if t == nil {
		panic("agent: nil tool " + key)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.tools[key]; exists {
		panic("agent: duplicate tool " + key)
	}
	r.tools[key] = t
}

// Get returns the tool registered under key.
func (r *ToolRegistry) Get(key string) (Tool, bool) {
	if r == nil {
		return nil, false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	t, ok := r.tools[key]
	return t, ok
}

// SchemasFor returns the wire schemas for the listed keys, ignoring any
// unknown keys so a stale conversation snapshot does not abort a turn.
func (r *ToolRegistry) SchemasFor(keys []string) []provider.ToolSchema {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]provider.ToolSchema, 0, len(keys))
	seen := map[string]struct{}{}
	for _, k := range keys {
		k = strings.TrimSpace(k)
		if k == "" {
			continue
		}
		if _, ok := seen[k]; ok {
			continue
		}
		seen[k] = struct{}{}
		if t, ok := r.tools[k]; ok {
			out = append(out, t.Schema())
		}
	}
	return out
}

// Keys returns all registered tool keys in stable (insertion-ish) order.
func (r *ToolRegistry) Keys() []string {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.tools))
	for k := range r.tools {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// ErrToolNotFound is returned by dispatch when the model asks for a tool
// not in the conversation's allow-list.
var ErrToolNotFound = errors.New("agent: tool not found")

// ErrToolNotAllowed is returned when a model asks for a registered tool
// that is not in the conversation snapshot's allow-list.
var ErrToolNotAllowed = errors.New("agent: tool not allowed")

// ErrToolTimeout wraps the per-tool deadline.
var ErrToolTimeout = errors.New("agent: tool timed out")

// DefaultToolTimeout caps each tool call. Tools may shorten it via ctx.
const DefaultToolTimeout = 10 * time.Second

// dispatch invokes the named tool with bounded timeout and JSON-encodes a
// consistent error string on failure so the model can recover.
func dispatch(ctx context.Context, reg *ToolRegistry, allowed map[string]struct{}, key, args string) (string, error) {
	if key == "" {
		return "tool error: missing tool name", nil
	}
	if _, ok := allowed[key]; !ok {
		return "tool error: " + ErrToolNotAllowed.Error(), nil
	}
	t, ok := reg.Get(key)
	if !ok {
		return "tool error: " + ErrToolNotFound.Error(), nil
	}
	tctx, cancel := context.WithTimeout(ctx, DefaultToolTimeout)
	defer cancel()
	out, err := t.Call(tctx, args)
	if err != nil {
		// Return a structured error message to the model rather than failing
		// the whole turn; this lets it apologise / retry gracefully.
		if errors.Is(tctx.Err(), context.DeadlineExceeded) {
			return "tool timed out", nil
		}
		return "tool error: " + err.Error(), nil
	}
	return out, nil
}

// --- built-in tools -------------------------------------------------------

// Builtin registers the default tool set: clock, echo. Per-module tools
// (vault.search, mail.draft) are registered separately and gated by
// feature flags.
func Builtin(reg *ToolRegistry) {
	reg.Register("clock", clockTool{})
	reg.Register("echo", echoTool{})
}

// clockTool reports the current server time. It is a safe smoke-test tool
// that never touches user data.
type clockTool struct{}

func (clockTool) Schema() provider.ToolSchema {
	return provider.ToolSchema{
		Name:        "clock",
		Description: "Report the current server time in RFC3339 format. Use whenever the user asks what time or date it is.",
		Parameters: map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		},
	}
}

func (clockTool) Call(ctx context.Context, _ string) (string, error) {
	return time.Now().UTC().Format(time.RFC3339), nil
}

// echoTool returns its input verbatim. Useful as a CI smoke test.
type echoTool struct{}

func (echoTool) Schema() provider.ToolSchema {
	return provider.ToolSchema{
		Name:        "echo",
		Description: "Echo the provided message back. Useful for testing tool dispatch.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"message": map[string]any{"type": "string"},
			},
			"required": []string{"message"},
		},
	}
}

func (echoTool) Call(_ context.Context, args string) (string, error) {
	var in struct {
		Message string `json:"message"`
	}
	if err := json.Unmarshal([]byte(args), &in); err != nil {
		return "", fmt.Errorf("invalid args: %w", err)
	}
	return in.Message, nil
}

// EncodeArgs is a tiny helper for in-process tests that need to produce
// the JSON arguments string expected by Tool.Call.
func EncodeArgs(m map[string]any) string {
	b, _ := json.Marshal(m)
	return string(b)
}

// PrettyArgs returns the arguments pretty-printed for display, falling
// back to the raw string when not valid JSON.
func PrettyArgs(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "{}"
	}
	var anyVal any
	if err := json.Unmarshal([]byte(trimmed), &anyVal); err != nil {
		return raw
	}
	b, err := json.MarshalIndent(anyVal, "", "  ")
	if err != nil {
		return raw
	}
	return string(b)
}
