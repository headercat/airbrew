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

// TestOpenAIDriverStream verifies the OpenAI driver parses the SSE
// stream, including tool_call fragments and the trailing usage chunk.
func TestOpenAIDriverStream(t *testing.T) {
	const resp = `data: {"choices":[{"delta":{"role":"assistant","content":"Hel"}}]}

data: {"choices":[{"delta":{"content":"lo"}}]}

data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","function":{"name":"clock","arguments":""}}]}}]}

data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{}"}}]}}]}

data: {"choices":[],"usage":{"prompt_tokens":10,"completion_tokens":5}}

data: [DONE]

`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(resp))
	}))
	defer srv.Close()

	cli, err := OpenAIDriver{}.Build(Config{
		Direction: DirectionChat, Driver: "openai",
		BaseURL: srv.URL, Model: "gpt-test", APIKey: "sk-x",
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	ch := cli.ChatStream(context.Background(), Request{Model: "gpt-test"})
	var (
		content  strings.Builder
		toolID   string
		toolName string
		toolArgs strings.Builder
		gotUsage bool
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
				gotUsage = true
				if d.Usage.PromptTokens != 10 || d.Usage.CompletionTokens != 5 {
					t.Fatalf("usage wrong: %+v", d.Usage)
				}
			}
		case DeltaError:
			t.Fatalf("delta error: %v", d.Err)
		}
	}
	if content.String() != "Hello" {
		t.Fatalf("content: %q", content.String())
	}
	if toolID != "call_1" || toolName != "clock" {
		t.Fatalf("tool start wrong: %s / %s", toolID, toolName)
	}
	if toolArgs.String() != "{}" {
		t.Fatalf("tool args: %q", toolArgs.String())
	}
	if !gotUsage {
		t.Fatal("usage chunk not received")
	}
}

// TestOpenAIDriverNeedsKey asserts the driver refuses to build without
// an API key (defence-in-depth against misconfiguration).
func TestOpenAIDriverNeedsKey(t *testing.T) {
	d := OpenAIDriver{}
	if _, err := d.Build(Config{Driver: "openai", Model: "x"}); err == nil {
		t.Fatal("expected error without api key")
	}
}

// TestOpenAIDriverUpstreamError asserts that a non-2xx response surfaces
// as a DeltaError rather than hanging the channel.
func TestOpenAIDriverUpstreamError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"invalid_api_key"}`, http.StatusUnauthorized)
	}))
	defer srv.Close()
	cli, _ := OpenAIDriver{}.Build(Config{
		Driver: "openai", BaseURL: srv.URL, Model: "x", APIKey: "sk-x",
	})
	ch := cli.ChatStream(context.Background(), Request{Model: "x"})
	var hit bool
	for d := range ch {
		if d.Kind == DeltaError {
			hit = true
			if !strings.Contains(d.Err.Error(), "401") {
				t.Fatalf("expected status in error, got %v", d.Err)
			}
		}
	}
	if !hit {
		t.Fatal("expected a DeltaError frame")
	}
}

func TestOpenAIDriverUnexpectedEOF(t *testing.T) {
	const resp = `data: {"choices":[{"delta":{"content":"partial"}}]}

`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(resp))
	}))
	defer srv.Close()
	cli, _ := OpenAIDriver{}.Build(Config{
		Driver: "openai", BaseURL: srv.URL, Model: "x", APIKey: "sk-x",
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

func TestOpenAIDriverInBandError(t *testing.T) {
	const resp = `data: {"error":{"message":"bad request","type":"invalid_request_error"}}

`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(resp))
	}))
	defer srv.Close()
	cli, _ := OpenAIDriver{}.Build(Config{
		Driver: "openai", BaseURL: srv.URL, Model: "x", APIKey: "sk-x",
	})
	ch := cli.ChatStream(context.Background(), Request{Model: "x"})
	var hit bool
	for d := range ch {
		if d.Kind == DeltaError {
			hit = true
			if !strings.Contains(d.Err.Error(), "bad request") {
				t.Fatalf("expected provider message, got %v", d.Err)
			}
		}
	}
	if !hit {
		t.Fatal("expected a DeltaError frame")
	}
}
