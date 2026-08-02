// Package provider — ollama.go
//
// Driver for Ollama's local /api/chat endpoint. The wire format is NDJSON
// streamed line-by-line; tool calls arrive as a single "tool_calls" array
// on the final message (Ollama does not stream tool args incrementally
// today, so we surface them as one DeltaToolCallArgs).
package provider

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const ollamaDefaultModel = "llama3.1"
const ollamaDefaultBaseURL = "http://localhost:11434"
const ollamaDefaultTimeout = 300 * time.Second

// OllamaDriver builds Ollama clients. The zero value is usable; it is
// registered at init under "ollama".
type OllamaDriver struct{}

func (OllamaDriver) DefaultModel() string { return ollamaDefaultModel }
func (OllamaDriver) NeedsAPIKey() bool    { return false }
func (OllamaDriver) Build(cfg Config) (LLMClient, error) {
	if cfg.BaseURL == "" {
		cfg.BaseURL = ollamaDefaultBaseURL
	}
	if cfg.Model == "" {
		cfg.Model = ollamaDefaultModel
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = ollamaDefaultTimeout
	}
	return &ollamaClient{cfg: cfg, http: &http.Client{Timeout: cfg.Timeout}}, nil
}

type ollamaClient struct {
	cfg  Config
	http *http.Client
}

func (c *ollamaClient) Model() string { return c.cfg.Model }

func (c *ollamaClient) ChatStream(ctx context.Context, req Request) <-chan Delta {
	out := make(chan Delta, 16)
	go func() {
		defer close(out)
		body, err := c.buildBody(req)
		if err != nil {
			_ = sendDelta(ctx, out, Delta{Kind: DeltaError, Err: err})
			return
		}
		url := strings.TrimRight(c.cfg.BaseURL, "/") + "/api/chat"
		httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
		if err != nil {
			_ = sendDelta(ctx, out, Delta{Kind: DeltaError, Err: err})
			return
		}
		httpReq.Header.Set("Content-Type", "application/json")
		httpReq.Header.Set("Accept", "application/x-ndjson")

		resp, err := c.http.Do(httpReq)
		if err != nil {
			_ = sendDelta(ctx, out, Delta{Kind: DeltaError, Err: err})
			return
		}
		defer resp.Body.Close()
		if resp.StatusCode/100 != 2 {
			_ = sendDelta(ctx, out, Delta{Kind: DeltaError, Err: c.httpError(resp)})
			return
		}
		c.streamNDJSON(ctx, resp.Body, out)
	}()
	return out
}

type ollamaReq struct {
	Model    string          `json:"model"`
	Messages []ollamaMessage `json:"messages"`
	Tools    []ollamaTool    `json:"tools,omitempty"`
	Stream   bool            `json:"stream"`
	Options  ollamaOptions   `json:"options,omitempty"`
}

type ollamaMessage struct {
	Role      string       `json:"role"`
	Content   string       `json:"content"`
	ToolCalls []ollamaTool `json:"tool_calls,omitempty"`
}

// ollamaTool is overloaded: in the request, it is the schema; in a tool
// message reply, it carries the executed function + raw arguments. Ollama
// uses the same struct shape for both.
type ollamaTool struct {
	Type     string         `json:"type,omitempty"`
	Function ollamaFuncSpec `json:"function"`
}

type ollamaFuncSpec struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters,omitempty"`
	Arguments   string         `json:"arguments,omitempty"` // tool reply
}

type ollamaOptions struct {
	Temperature float32 `json:"temperature,omitempty"`
	NumPredict  int     `json:"num_predict,omitempty"`
}

func (c *ollamaClient) buildBody(req Request) ([]byte, error) {
	body := ollamaReq{
		Model: orDefault(req.Model, c.cfg.Model), Stream: true,
		Options: ollamaOptions{Temperature: req.Temperature, NumPredict: req.MaxTokens},
	}
	for _, m := range req.Messages {
		om := ollamaMessage{Role: string(m.Role), Content: m.Content}
		for _, tc := range m.ToolCalls {
			om.ToolCalls = append(om.ToolCalls, ollamaTool{
				Type:     "function",
				Function: ollamaFuncSpec{Name: tc.Name, Arguments: tc.Args},
			})
		}
		body.Messages = append(body.Messages, om)
	}
	for _, t := range req.Tools {
		body.Tools = append(body.Tools, ollamaTool{
			Type: "function",
			Function: ollamaFuncSpec{
				Name: t.Name, Description: t.Description, Parameters: t.Parameters,
			},
		})
	}
	return json.Marshal(body)
}

type ollamaChunk struct {
	Message    ollamaMessage `json:"message"`
	Done       bool          `json:"done"`
	PromptEval int           `json:"prompt_eval_count"`
	Completion int           `json:"eval_count"`
	Error      string        `json:"error,omitempty"`
}

func (c *ollamaClient) streamNDJSON(ctx context.Context, body io.Reader, out chan<- Delta) {
	br := bufio.NewReaderSize(body, 16<<10)
	var (
		toolIdx int
	)
	for {
		select {
		case <-ctx.Done():
			_ = sendDelta(ctx, out, Delta{Kind: DeltaError, Err: ctx.Err()})
			return
		default:
		}
		line, err := br.ReadString('\n')
		if err != nil {
			if err == io.EOF {
				_ = sendDelta(ctx, out, Delta{Kind: DeltaError, Err: io.ErrUnexpectedEOF})
				return
			}
			_ = sendDelta(ctx, out, Delta{Kind: DeltaError, Err: err})
			return
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var ch ollamaChunk
		if err := json.Unmarshal([]byte(line), &ch); err != nil {
			continue
		}
		if ch.Error != "" {
			_ = sendDelta(ctx, out, Delta{Kind: DeltaError, Err: fmt.Errorf("ollama stream error: %s", ch.Error)})
			return
		}
		if ch.Message.Content != "" {
			if !sendDelta(ctx, out, Delta{Kind: DeltaContent, Content: ch.Message.Content}) {
				return
			}
		}
		for _, tc := range ch.Message.ToolCalls {
			if !sendDelta(ctx, out, Delta{
				Kind: DeltaToolCallStart, Index: toolIdx,
				ToolCallID: fmt.Sprintf("call_%d", toolIdx),
				ToolName:   tc.Function.Name,
			}) {
				return
			}
			if !sendDelta(ctx, out, Delta{
				Kind: DeltaToolCallArgs, Index: toolIdx,
				Content: tc.Function.Arguments,
			}) {
				return
			}
			toolIdx++
		}
		if ch.Done {
			_ = sendDelta(ctx, out, Delta{Kind: DeltaDone, Usage: &Usage{
				PromptTokens: ch.PromptEval, CompletionTokens: ch.Completion,
			}})
			return
		}
	}
}

func (c *ollamaClient) httpError(resp *http.Response) error {
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
	return &upstreamError{status: resp.StatusCode, body: string(body)}
}

func init() { Register("ollama", OllamaDriver{}) }
