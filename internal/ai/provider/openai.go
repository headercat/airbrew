// Package provider — openai.go
//
// Driver for the OpenAI Chat Completions API and its many compatible
// endpoints (Groq, OpenRouter, Together, vLLM, llama.cpp server, ...).
// Streaming uses the standard "stream: true" SSE response; tool calls are
// reconstructed from the streamed delta.tool_calls fragments.
package provider

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const openAIDefaultModel = "gpt-4o-mini"
const openAIDefaultBaseURL = "https://api.openai.com/v1"
const openAIDefaultTimeout = 120 * time.Second

// OpenAIDriver builds OpenAI-compatible LLMClients. The zero value is
// usable; it is registered at init under "openai".
type OpenAIDriver struct{}

func (OpenAIDriver) DefaultModel() string { return openAIDefaultModel }
func (OpenAIDriver) NeedsAPIKey() bool    { return true }
func (OpenAIDriver) Build(cfg Config) (LLMClient, error) {
	if cfg.APIKey == "" {
		return nil, fmt.Errorf("%w: openai requires api_key", ErrInvalidConfig)
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = openAIDefaultBaseURL
	}
	if cfg.Model == "" {
		cfg.Model = openAIDefaultModel
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = openAIDefaultTimeout
	}
	return &openAIClient{cfg: cfg, http: &http.Client{Timeout: cfg.Timeout}}, nil
}

type openAIClient struct {
	cfg  Config
	http *http.Client
}

func (c *openAIClient) Model() string { return c.cfg.Model }

func (c *openAIClient) ChatStream(ctx context.Context, req Request) <-chan Delta {
	out := make(chan Delta, 16)
	go func() {
		defer close(out)
		body, err := c.buildBody(req)
		if err != nil {
			_ = sendDelta(ctx, out, Delta{Kind: DeltaError, Err: err})
			return
		}
		url := strings.TrimRight(c.cfg.BaseURL, "/") + "/chat/completions"
		httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
		if err != nil {
			_ = sendDelta(ctx, out, Delta{Kind: DeltaError, Err: err})
			return
		}
		httpReq.Header.Set("Content-Type", "application/json")
		httpReq.Header.Set("Authorization", "Bearer "+c.cfg.APIKey)
		httpReq.Header.Set("Accept", "text/event-stream")

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
		c.streamSSE(ctx, resp.Body, out)
	}()
	return out
}

type openAIReq struct {
	Model       string        `json:"model"`
	Messages    []openAIMsg   `json:"messages"`
	Tools       []openAITool  `json:"tools,omitempty"`
	Temperature float32       `json:"temperature,omitempty"`
	MaxTokens   int           `json:"max_tokens,omitempty"`
	Stream      bool          `json:"stream"`
	StreamOpts  *openAIStream `json:"stream_options,omitempty"`
}

type openAIMsg struct {
	Role       string           `json:"role"`
	Content    string           `json:"content,omitempty"`
	ToolCalls  []openAIToolCall `json:"tool_calls,omitempty"`
	ToolCallID string           `json:"tool_call_id,omitempty"`
}

type openAITool struct {
	Type     string         `json:"type"` // always "function"
	Function openAIToolFunc `json:"function"`
}

type openAIToolFunc struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
}

type openAIToolCall struct {
	ID       string             `json:"id"`
	Type     string             `json:"type"` // "function"
	Function openAIToolCallFunc `json:"function"`
}

type openAIToolCallFunc struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type openAIStream struct {
	IncludeUsage bool `json:"include_usage"`
}

func (c *openAIClient) buildBody(req Request) ([]byte, error) {
	msgs := make([]openAIMsg, 0, len(req.Messages))
	for _, m := range req.Messages {
		om := openAIMsg{
			Role: string(m.Role), Content: m.Content,
			ToolCallID: m.ToolCallID,
		}
		for _, tc := range m.ToolCalls {
			om.ToolCalls = append(om.ToolCalls, openAIToolCall{
				ID: tc.ID, Type: "function",
				Function: openAIToolCallFunc{Name: tc.Name, Arguments: tc.Args},
			})
		}
		msgs = append(msgs, om)
	}
	body := openAIReq{
		Model:    orDefault(req.Model, c.cfg.Model),
		Messages: msgs, Temperature: req.Temperature,
		MaxTokens: req.MaxTokens, Stream: true,
		StreamOpts: &openAIStream{IncludeUsage: true},
	}
	for _, t := range req.Tools {
		body.Tools = append(body.Tools, openAITool{
			Type: "function",
			Function: openAIToolFunc{
				Name: t.Name, Description: t.Description, Parameters: t.Parameters,
			},
		})
	}
	return json.Marshal(body)
}

// streamSSE parses the OpenAI SSE stream. Each chunk is a JSON object whose
// "choices[0].delta" carries either content or tool-call fragments; the
// final chunk (with choices empty and usage set) carries the token totals.
func (c *openAIClient) streamSSE(ctx context.Context, body io.Reader, out chan<- Delta) {
	toolCalls := map[int]*runningToolCall{}
	br := bufio.NewReaderSize(body, 16<<10)
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
		line = strings.TrimRight(line, "\r\n")
		if line == "" || !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			_ = sendDelta(ctx, out, Delta{Kind: DeltaDone})
			return
		}
		var chunk openAIChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue // tolerate keep-alive noise
		}
		if chunk.Error != nil {
			_ = sendDelta(ctx, out, Delta{Kind: DeltaError, Err: fmt.Errorf("openai stream error: %s", chunk.Error.Message)})
			return
		}
		if chunk.Usage != nil {
			_ = sendDelta(ctx, out, Delta{Kind: DeltaDone, Usage: &Usage{
				PromptTokens:     chunk.Usage.PromptTokens,
				CompletionTokens: chunk.Usage.CompletionTokens,
			}})
			return
		}
		if len(chunk.Choices) == 0 {
			continue
		}
		ch := chunk.Choices[0]
		if ch.Delta.Content != "" {
			if !sendDelta(ctx, out, Delta{Kind: DeltaContent, Content: ch.Delta.Content}) {
				return
			}
		}
		for _, tcd := range ch.Delta.ToolCalls {
			idx := tcd.Index
			rc, ok := toolCalls[idx]
			if !ok {
				rc = &runningToolCall{index: idx}
				toolCalls[idx] = rc
			}
			if tcd.ID != "" {
				rc.id = tcd.ID
			}
			if tcd.Function != nil {
				if tcd.Function.Name != "" {
					rc.name = tcd.Function.Name
				}
				if tcd.Function.Arguments != "" {
					rc.args.WriteString(tcd.Function.Arguments)
				}
				// First sight of this call: announce it once we know the id.
				if rc.id != "" && !rc.announced {
					rc.announced = true
					if !sendDelta(ctx, out, Delta{
						Kind: DeltaToolCallStart, Index: rc.index,
						ToolCallID: rc.id, ToolName: rc.name,
					}) {
						return
					}
				}
				if tcd.Function.Arguments != "" {
					if !sendDelta(ctx, out, Delta{Kind: DeltaToolCallArgs, Index: rc.index, Content: tcd.Function.Arguments}) {
						return
					}
				}
			}
		}
	}
}

type runningToolCall struct {
	index     int
	id        string
	name      string
	args      bytes.Buffer
	announced bool
}

type openAIChunk struct {
	Choices []openAIChoice `json:"choices"`
	Usage   *openAIUsage   `json:"usage,omitempty"`
	Error   *openAIError   `json:"error,omitempty"`
}

type openAIError struct {
	Message string `json:"message"`
	Type    string `json:"type,omitempty"`
	Code    string `json:"code,omitempty"`
}

type openAIChoice struct {
	Delta openAIDelta `json:"delta"`
}

type openAIDelta struct {
	Role      string            `json:"role,omitempty"`
	Content   string            `json:"content,omitempty"`
	ToolCalls []openAIToolDelta `json:"tool_calls,omitempty"`
}

type openAIToolDelta struct {
	Index    int                  `json:"index"`
	ID       string               `json:"id"`
	Function *openAIToolDeltaFunc `json:"function,omitempty"`
}

type openAIToolDeltaFunc struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type openAIUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
}

func (c *openAIClient) httpError(resp *http.Response) error {
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
	return &upstreamError{status: resp.StatusCode, body: string(body)}
}

// ErrUpstream signals a non-2xx response from the provider. The runtime
// surfaces it via DeltaError so the SPA can show a friendly message.
var ErrUpstream = errors.New("provider: upstream error")

func orDefault(s, def string) string {
	if s != "" {
		return s
	}
	return def
}

func init() { Register("openai", OpenAIDriver{}) }
