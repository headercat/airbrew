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

// TestOllamaDriverStream verifies the NDJSON parser handles content
// deltas, tool_calls, and the final done:true chunk with usage.
func TestOllamaDriverStream(t *testing.T) {
	const resp = `{"message":{"role":"assistant","content":"Hel"},"done":false}
{"message":{"role":"assistant","content":"lo"},"done":false}
{"message":{"role":"assistant","tool_calls":[{"function":{"name":"clock","arguments":"{}"}}]},"done":false}
{"message":{"role":"assistant"},"done":true,"prompt_eval_count":8,"eval_count":3}
`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		_, _ = w.Write([]byte(resp))
	}))
	defer srv.Close()

	d := OllamaDriver{}
	cli, err := d.Build(Config{
		Direction: DirectionChat, Driver: "ollama",
		BaseURL: srv.URL, Model: "llama-test",
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	ch := cli.ChatStream(context.Background(), Request{Model: "llama-test"})
	var (
		content     strings.Builder
		toolName    string
		toolArgs    strings.Builder
		gotPrompt   int
		gotComplete int
	)
	for d := range ch {
		switch d.Kind {
		case DeltaContent:
			content.WriteString(d.Content)
		case DeltaToolCallStart:
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
	if toolName != "clock" || toolArgs.String() != "{}" {
		t.Fatalf("tool wrong: %s / %q", toolName, toolArgs.String())
	}
	if gotPrompt != 8 || gotComplete != 3 {
		t.Fatalf("usage wrong: prompt=%d complete=%d", gotPrompt, gotComplete)
	}
}

// TestOllamaDriverNoKey ensures Ollama runs without an API key (local).
func TestOllamaDriverNoKey(t *testing.T) {
	d := OllamaDriver{}
	if d.NeedsAPIKey() {
		t.Fatal("ollama should not require api key")
	}
	cli, err := d.Build(Config{Driver: "ollama", BaseURL: "http://x", Model: "x"})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if cli.Model() != "x" {
		t.Fatalf("model: %s", cli.Model())
	}
}

func TestOllamaDriverUnexpectedEOF(t *testing.T) {
	const resp = `{"message":{"role":"assistant","content":"partial"},"done":false}
`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		_, _ = w.Write([]byte(resp))
	}))
	defer srv.Close()
	cli, _ := OllamaDriver{}.Build(Config{Driver: "ollama", BaseURL: srv.URL, Model: "x"})
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

func TestOllamaDriverInBandError(t *testing.T) {
	const resp = `{"error":"model unavailable"}
`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		_, _ = w.Write([]byte(resp))
	}))
	defer srv.Close()
	cli, _ := OllamaDriver{}.Build(Config{Driver: "ollama", BaseURL: srv.URL, Model: "x"})
	ch := cli.ChatStream(context.Background(), Request{Model: "x"})
	var hit bool
	for d := range ch {
		if d.Kind == DeltaError {
			hit = true
			if !strings.Contains(d.Err.Error(), "model unavailable") {
				t.Fatalf("expected provider message, got %v", d.Err)
			}
		}
	}
	if !hit {
		t.Fatal("expected a DeltaError frame")
	}
}
