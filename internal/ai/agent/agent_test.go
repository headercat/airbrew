package agent

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/headercat/airbrew/internal/ai/conv"
	"github.com/headercat/airbrew/internal/ai/provider"
)

// fakeProvider is a scriptable LLMClient for runtime tests. Each entry in
// script is returned as one assistant turn; the runtime loops until a
// turn has no tool calls.
type fakeProvider struct {
	model   string
	script  []fakeTurn
	calls   int
}

type fakeTurn struct {
	content   string
	toolCalls []provider.ToolCall
	usage     provider.Usage
}

func (f *fakeProvider) Model() string { return f.model }
func (f *fakeProvider) ChatStream(_ context.Context, _ provider.Request) <-chan provider.Delta {
	out := make(chan provider.Delta, 8)
	go func() {
		defer close(out)
		if f.calls >= len(f.script) {
			out <- provider.Delta{Kind: provider.DeltaDone}
			return
		}
		turn := f.script[f.calls]
		f.calls++
		// Emit content word-by-word to exercise the streaming path.
		for _, w := range strings.Fields(turn.content) {
			out <- provider.Delta{Kind: provider.DeltaContent, Content: w + " "}
		}
		for i, tc := range turn.toolCalls {
			out <- provider.Delta{
				Kind: provider.DeltaToolCallStart, Index: i,
				ToolCallID: tc.ID, ToolName: tc.Name,
			}
			out <- provider.Delta{
				Kind: provider.DeltaToolCallArgs, Index: i, Content: tc.Args,
			}
		}
		out <- provider.Delta{Kind: provider.DeltaDone, Usage: &turn.usage}
	}()
	return out
}

func newRuntime(t *testing.T, script []fakeTurn) (*Runtime, *conv.Service, string, string) {
	t.Helper()
	r, uid, agentID := newTestService(t)
	rt := &Runtime{
		conv:  r,
		tools: NewToolRegistry(),
		ResolveProvider: func(context.Context) (provider.LLMClient, error) {
			return &fakeProvider{model: "fake-1", script: script}, nil
		},
		Now: func() time.Time { return time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC) },
	}
	return rt, r, uid, agentID
}

func TestRuntimeSimpleTurn(t *testing.T) {
	rt, _, uid, agentID := newRuntime(t, []fakeTurn{
		{content: "hello world", usage: provider.Usage{PromptTokens: 10, CompletionTokens: 5}},
	})
	c, err := rt.conv.Create(context.Background(), conv.CreateInput{
		UserID: uid, AgentID: agentID, SnapModel: "fake-1", SnapTools: []string{},
		SnapMaxTurns: 6,
	})
	if err != nil {
		t.Fatalf("create conv: %v", err)
	}
	events := collectEvents(rt.Run(context.Background(), RunInput{
		UserID: uid, ConversationID: c.ID, UserMessage: "hi",
		Spec: Spec{Model: "fake-1", MaxTurns: 3},
	}))
	if !events.done {
		t.Fatalf("expected done event, got: %+v", events)
	}
	if events.deltaText() != "hello world " {
		t.Fatalf("unexpected delta text: %q", events.deltaText())
	}
	// History should now have user + assistant rows.
	hist, _ := rt.conv.History(context.Background(), uid, c.ID)
	if len(hist) != 2 || hist[1].Role != provider.RoleAssistant {
		t.Fatalf("history wrong: %+v", hist)
	}
}

func TestRuntimeToolDispatchLoop(t *testing.T) {
	rt, _, uid, agentID := newRuntime(t, []fakeTurn{
		{
			content:   "let me check",
			toolCalls: []provider.ToolCall{{ID: "call_1", Name: "clock", Args: "{}"}},
			usage:     provider.Usage{PromptTokens: 8, CompletionTokens: 4},
		},
		{
			content: "the time is noon",
			usage:   provider.Usage{PromptTokens: 12, CompletionTokens: 6},
		},
	})
	Builtin(rt.tools) // register clock
	c, _ := rt.conv.Create(context.Background(), conv.CreateInput{
		UserID: uid, AgentID: agentID, SnapModel: "fake-1",
		SnapTools: []string{"clock"}, SnapMaxTurns: 6,
	})
	events := collectEvents(rt.Run(context.Background(), RunInput{
		UserID: uid, ConversationID: c.ID, UserMessage: "what time?",
		Spec: Spec{Model: "fake-1", MaxTurns: 4, Tools: []string{"clock"}},
	}))
	if !events.done {
		t.Fatalf("expected done event, got: %+v", events)
	}
	if len(events.toolCalls) != 1 || events.toolCalls[0].name != "clock" {
		t.Fatalf("expected one clock tool call, got %+v", events.toolCalls)
	}
	if events.deltaText() != "let me check the time is noon " {
		t.Fatalf("delta content mismatch: %q", events.deltaText())
	}
	// The transcript should have 4 rows: user, assistant(tool), tool, assistant(final).
	hist, _ := rt.conv.History(context.Background(), uid, c.ID)
	if len(hist) != 4 {
		t.Fatalf("expected 4 history rows, got %d: %+v", len(hist), hist)
	}
	if hist[2].Role != provider.RoleTool || hist[2].ToolCallID != "call_1" {
		t.Fatalf("tool row wrong: %+v", hist[2])
	}
}

func TestRuntimeHitsTurnCap(t *testing.T) {
	// Every turn emits a tool call → the runtime must terminate at max_turns.
	script := make([]fakeTurn, 10)
	for i := range script {
		script[i] = fakeTurn{
			toolCalls: []provider.ToolCall{{ID: "c", Name: "clock", Args: "{}"}},
		}
	}
	rt, _, uid, agentID := newRuntime(t, script)
	Builtin(rt.tools)
	c, _ := rt.conv.Create(context.Background(), conv.CreateInput{
		UserID: uid, AgentID: agentID, SnapModel: "fake-1",
		SnapTools: []string{"clock"}, SnapMaxTurns: 6,
	})
	events := collectEvents(rt.Run(context.Background(), RunInput{
		UserID: uid, ConversationID: c.ID, UserMessage: "loop",
		Spec: Spec{Model: "fake-1", MaxTurns: 3, Tools: []string{"clock"}},
	}))
	if events.err == "" {
		t.Fatalf("expected error after hitting turn cap, got clean done")
	}
}

func TestRuntimeProviderError(t *testing.T) {
	rt := &Runtime{
		conv:  mustService(t),
		tools: NewToolRegistry(),
		ResolveProvider: func(context.Context) (provider.LLMClient, error) {
			return nil, errors.New("no provider configured")
		},
		Now: time.Now,
	}
	events := collectEvents(rt.Run(context.Background(), RunInput{
		UserID: "u1", ConversationID: "nope", UserMessage: "x",
	}))
	if events.err == "" {
		t.Fatalf("expected error event, got %+v", events)
	}
}

func TestBuildProviderMessagesReplacesStaleSystem(t *testing.T) {
	hist := []provider.Message{
		{Role: provider.RoleSystem, Content: "old system"},
		{Role: provider.RoleUser, Content: "hi"},
	}
	out := buildProviderMessages("new system", hist)
	if len(out) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(out))
	}
	if out[0].Role != provider.RoleSystem || out[0].Content != "new system" {
		t.Fatalf("system not replaced: %+v", out[0])
	}
	if out[1].Content != "hi" {
		t.Fatalf("user row corrupted: %+v", out[1])
	}
}

// --- helpers --------------------------------------------------------------

type collected struct {
	deltas   []string
	toolCalls []struct {
		id, name, result string
	}
	done    bool
	doneMsg string
	err     string
}

func collectEvents(ch <-chan Event) *collected {
	c := &collected{}
	for ev := range ch {
		switch ev.Kind {
		case EventDelta:
			c.deltas = append(c.deltas, ev.Content)
		case EventTool:
			c.toolCalls = append(c.toolCalls, struct {
				id, name, result string
			}{ev.ToolCallID, ev.ToolName, ev.ToolResult})
		case EventDone:
			c.done = true
			c.doneMsg = ev.MessageID
		case EventError:
			if ev.Err != nil {
				c.err = ev.Err.Code + ": " + ev.Err.Description
			}
		}
	}
	return c
}

func (c *collected) deltaText() string {
	if c == nil {
		return ""
	}
	return strings.Join(c.deltas, "")
}
