package provider

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestAnthropicDriverStream verifies the Anthropic Messages SSE event
// sequence: message_start (input tokens) → text deltas → tool_use
// content_block_start + input_json_delta → message_delta (output
// tokens) → message_stop.
func TestAnthropicDriverStream(t *testing.T) {
	const resp = `event: message_start
data: {"type":"message_start","message":{"id":"msg_1","model":"claude","usage":{"input_tokens":7,"output_tokens":0}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hel"}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"lo"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: content_block_start
data: {"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"tool_1","name":"clock","input":{}}}

event: content_block_delta
data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{}"}}

event: content_block_stop
data: {"type":"content_block_stop","index":1}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":4}}

event: message_stop
data: {"type":"message_stop"}

`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(resp))
	}))
	defer srv.Close()

	d := AnthropicDriver{}
	cli, err := d.Build(Config{
		Direction: DirectionChat, Driver: "anthropic",
		BaseURL: srv.URL, Model: "claude-test", APIKey: "sk-x",
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	ch := cli.ChatStream(context.Background(), Request{Model: "claude-test"})
	var (
		content     strings.Builder
		toolName    string
		toolID      string
		toolArgs    strings.Builder
		gotPrompt   int
		gotComplete int
	)
	for d := range ch {
		switch d.Kind {
		case DeltaContent:
			content.WriteString(d.Content)
		case DeltaToolCallStart:
			toolID = d.ToolCallID
			toolName = d.ToolName
		case DeltaToolCallArgs:
			toolArgs.WriteString(d.Content)
		case DeltaDone:
			if d.Usage != nil {
				gotPrompt = d.Usage.PromptTokens
				gotComplete = d.Usage.CompletionTokens
			}
		case DeltaError:
			t.Fatalf("delta error: %v", d.Err)
		}
	}
	if content.String() != "Hello" {
		t.Fatalf("content: %q", content.String())
	}
	if toolID != "tool_1" || toolName != "clock" {
		t.Fatalf("tool start wrong: %s / %s", toolID, toolName)
	}
	if toolArgs.String() != "{}" {
		t.Fatalf("tool args: %q", toolArgs.String())
	}
	if gotPrompt != 7 || gotComplete != 4 {
		t.Fatalf("usage wrong: prompt=%d complete=%d", gotPrompt, gotComplete)
	}
}

func TestAnthropicDriverNeedsKey(t *testing.T) {
	d := AnthropicDriver{}
	if _, err := d.Build(Config{Driver: "anthropic", Model: "x"}); err == nil {
		t.Fatal("expected error without api key")
	}
}

func TestAnthropicDriverUnexpectedEOF(t *testing.T) {
	const resp = `event: message_start
data: {"type":"message_start","message":{"id":"msg_1","model":"claude","usage":{"input_tokens":7,"output_tokens":0}}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"partial"}}

`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(resp))
	}))
	defer srv.Close()
	cli, _ := AnthropicDriver{}.Build(Config{
		Driver: "anthropic", BaseURL: srv.URL, Model: "x", APIKey: "sk-x",
	})
	ch := cli.ChatStream(context.Background(), Request{Model: "x"})
	var hit bool
	for d := range ch {
		if d.Kind == DeltaError {
			hit = true
			if !errors.Is(d.Err, io.ErrUnexpectedEOF) {
				t.Fatalf("expected unexpected EOF, got %v", d.Err)
			}
		}
	}
	if !hit {
		t.Fatal("expected a DeltaError frame")
	}
}
