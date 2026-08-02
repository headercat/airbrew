// Package provider — anthropic.go
//
// Driver for the Anthropic Messages API. Streaming uses SSE events of
// type "content_block_delta"; tool calls arrive as "input_json_delta"
// fragments under "tool_use" content blocks.
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

const anthropicDefaultModel = "claude-3-5-haiku-latest"
const anthropicDefaultBaseURL = "https://api.anthropic.com"
const anthropicDefaultTimeout = 120 * time.Second
const anthropicAPIVersion = "2023-06-01"

// AnthropicDriver builds Anthropic Messages clients. The zero value is
// usable; it is registered at init under "anthropic".
type AnthropicDriver struct{}

func (AnthropicDriver) DefaultModel() string { return anthropicDefaultModel }
func (AnthropicDriver) NeedsAPIKey() bool    { return true }
func (AnthropicDriver) Build(cfg Config) (LLMClient, error) {
	if cfg.APIKey == "" {
		return nil, fmt.Errorf("%w: anthropic requires api_key", ErrInvalidConfig)
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = anthropicDefaultBaseURL
	}
	if cfg.Model == "" {
		cfg.Model = anthropicDefaultModel
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = anthropicDefaultTimeout
	}
	return &anthropicClient{cfg: cfg, http: &http.Client{Timeout: cfg.Timeout}}, nil
}

type anthropicClient struct {
	cfg  Config
	http *http.Client
}

func (c *anthropicClient) Model() string { return c.cfg.Model }

func (c *anthropicClient) ChatStream(ctx context.Context, req Request) <-chan Delta {
	out := make(chan Delta, 16)
	go func() {
		defer close(out)
		body, err := c.buildBody(req)
		if err != nil {
			_ = sendDelta(ctx, out, Delta{Kind: DeltaError, Err: err})
			return
		}
		url := strings.TrimRight(c.cfg.BaseURL, "/") + "/v1/messages"
		httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
		if err != nil {
			_ = sendDelta(ctx, out, Delta{Kind: DeltaError, Err: err})
			return
		}
		httpReq.Header.Set("Content-Type", "application/json")
		httpReq.Header.Set("x-api-key", c.cfg.APIKey)
		httpReq.Header.Set("anthropic-version", anthropicAPIVersion)
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

type anthropicReq struct {
	Model       string             `json:"model"`
	System      string             `json:"system,omitempty"`
	Messages    []anthropicMessage `json:"messages"`
	Tools       []anthropicTool    `json:"tools,omitempty"`
	Temperature float32            `json:"temperature,omitempty"`
	MaxTokens   int                `json:"max_tokens"`
	Stream      bool               `json:"stream"`
}

type anthropicMessage struct {
	Role    string           `json:"role"`
	Content anthropicContent `json:"-"`
}

func (m anthropicMessage) MarshalJSON() ([]byte, error) {
	type wire struct {
		Role    string           `json:"role"`
		Content anthropicContent `json:"content"`
	}
	return json.Marshal(wire{Role: m.Role, Content: m.Content})
}

// anthropicContent is either a plain string (marshal) or a slice of typed
// blocks (tool_use / tool_result). We marshal manually so we can switch on
// shape without forcing callers into one form.
type anthropicContent struct {
	Plain  string
	Blocks []anthropicBlock
}

func (c anthropicContent) MarshalJSON() ([]byte, error) {
	if c.Blocks != nil {
		return json.Marshal(c.Blocks)
	}
	return json.Marshal(c.Plain)
}

type anthropicBlock struct {
	Type      string         `json:"type"`
	Text      string         `json:"text,omitempty"`
	ID        string         `json:"id,omitempty"`
	Name      string         `json:"name,omitempty"`
	Input     map[string]any `json:"input,omitempty"`
	ToolUseID string         `json:"tool_use_id,omitempty"`
	Content   string         `json:"content,omitempty"`
	IsError   bool           `json:"is_error,omitempty"`
}

func (b anthropicBlock) MarshalJSON() ([]byte, error) {
	m := map[string]any{"type": b.Type}
	if b.Text != "" {
		m["text"] = b.Text
	}
	if b.ID != "" {
		m["id"] = b.ID
	}
	if b.Name != "" {
		m["name"] = b.Name
	}
	if b.Type == "tool_use" {
		if b.Input == nil {
			b.Input = map[string]any{}
		}
		m["input"] = b.Input
	}
	if b.ToolUseID != "" {
		m["tool_use_id"] = b.ToolUseID
	}
	if b.Content != "" {
		m["content"] = b.Content
	}
	if b.IsError {
		m["is_error"] = b.IsError
	}
	return json.Marshal(m)
}

type anthropicTool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"input_schema"`
}

func (c *anthropicClient) buildBody(req Request) ([]byte, error) {
	out := anthropicReq{
		Model:       orDefault(req.Model, c.cfg.Model),
		Temperature: req.Temperature, Stream: true,
	}
	if req.MaxTokens <= 0 {
		out.MaxTokens = 4096
	} else {
		out.MaxTokens = req.MaxTokens
	}
	for _, m := range req.Messages {
		switch m.Role {
		case RoleSystem:
			if out.System == "" {
				out.System = m.Content
			} else {
				out.System += "\n\n" + m.Content
			}
			continue
		case RoleUser:
			out.Messages = append(out.Messages, anthropicMessage{
				Role: "user", Content: anthropicContent{Plain: m.Content},
			})
		case RoleAssistant:
			blocks := []anthropicBlock{}
			if m.Content != "" {
				blocks = append(blocks, anthropicBlock{Type: "text", Text: m.Content})
			}
			for _, tc := range m.ToolCalls {
				var input map[string]any
				if err := json.Unmarshal([]byte(tc.Args), &input); err != nil || input == nil {
					input = map[string]any{}
				}
				blocks = append(blocks, anthropicBlock{
					Type: "tool_use", ID: tc.ID, Name: tc.Name, Input: input,
				})
			}
			out.Messages = append(out.Messages, anthropicMessage{
				Role: "assistant", Content: anthropicContent{Blocks: blocks},
			})
		case RoleTool:
			out.Messages = append(out.Messages, anthropicMessage{
				Role: "user",
				Content: anthropicContent{Blocks: []anthropicBlock{{
					Type: "tool_result", ToolUseID: m.ToolCallID, Content: m.Content,
				}}},
			})
		}
	}
	for _, t := range req.Tools {
		out.Tools = append(out.Tools, anthropicTool{
			Name: t.Name, Description: t.Description, InputSchema: t.Parameters,
		})
	}
	return json.Marshal(out)
}

type anthropicEvent struct {
	Type         string           `json:"type"`
	Delta        json.RawMessage  `json:"delta,omitempty"`
	Message      anthropicMsgMeta `json:"message,omitempty"`
	Error        anthropicErr     `json:"error,omitempty"`
	Index        int              `json:"index,omitempty"`
	ContentBlock anthropicBlock   `json:"content_block,omitempty"`
}

type anthropicErr struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

type anthropicMsgMeta struct {
	ID    string         `json:"id"`
	Model string         `json:"model"`
	Usage anthropicUsage `json:"usage"`
}

type anthropicUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

type anthropicContentDelta struct {
	Type string `json:"type"`
	Text string `json:"text"`
	// tool_use deltas:
	PartialJSON string `json:"partial_json"`
}

// streamSSE parses the Anthropic event stream. State machine:
//   - message_start: emits nothing yet (usage may be partial).
//   - content_block_start: opens a tool_use block; we emit a
//     DeltaToolCallStart so the client can render the tool name plate.
//   - content_block_delta: text deltas emit DeltaContent; input_json_delta
//     fragments emit DeltaToolCallArgs.
//   - message_delta: final usage arrives here as usage.output_tokens; we
//     wait for this before emitting DeltaDone so the totals are complete.
//   - message_stop: terminal — we close the stream.
func (c *anthropicClient) streamSSE(ctx context.Context, body io.Reader, out chan<- Delta) {
	br := bufio.NewReaderSize(body, 16<<10)
	var (
		inputTokens  int
		outputTokens int
		// tool block index → id/name from content_block_start
		toolMeta = map[int]anthropicBlock{}
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
		line = strings.TrimRight(line, "\r\n")
		if line == "" || !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		var ev anthropicEvent
		if err := json.Unmarshal([]byte(data), &ev); err != nil {
			continue
		}
		switch ev.Type {
		case "error":
			_ = sendDelta(ctx, out, Delta{Kind: DeltaError, Err: fmt.Errorf("anthropic stream error: %s", ev.Error.Message)})
			return
		case "message_start":
			if ev.Message.Usage.InputTokens > 0 {
				inputTokens = ev.Message.Usage.InputTokens
			}
		case "content_block_start":
			if ev.ContentBlock.Type == "tool_use" {
				toolMeta[ev.Index] = ev.ContentBlock
				if !sendDelta(ctx, out, Delta{
					Kind: DeltaToolCallStart, Index: ev.Index,
					ToolCallID: ev.ContentBlock.ID, ToolName: ev.ContentBlock.Name,
				}) {
					return
				}
			}
		case "content_block_delta":
			var d anthropicContentDelta
			if json.Unmarshal(ev.Delta, &d) != nil {
				continue
			}
			switch d.Type {
			case "text_delta":
				if !sendDelta(ctx, out, Delta{Kind: DeltaContent, Content: d.Text}) {
					return
				}
			case "input_json_delta":
				if !sendDelta(ctx, out, Delta{Kind: DeltaToolCallArgs, Index: ev.Index, Content: d.PartialJSON}) {
					return
				}
			}
		case "message_delta":
			// ev.Delta carries a stop_reason; ev.Message.Usage carries the
			// final output_tokens count.
			var md struct {
				Usage anthropicUsage `json:"usage"`
			}
			// The wire shape embeds usage at top level under "usage".
			var probe struct {
				Usage anthropicUsage `json:"usage"`
			}
			_ = json.Unmarshal([]byte(data), &probe)
			md.Usage = probe.Usage
			if md.Usage.OutputTokens > 0 {
				outputTokens = md.Usage.OutputTokens
			}
		case "message_stop":
			_ = sendDelta(ctx, out, Delta{Kind: DeltaDone, Usage: &Usage{
				PromptTokens: inputTokens, CompletionTokens: outputTokens,
			}})
			return
		}
	}
}

func (c *anthropicClient) httpError(resp *http.Response) error {
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
	return &upstreamError{status: resp.StatusCode, body: string(body)}
}

func init() { Register("anthropic", AnthropicDriver{}) }
