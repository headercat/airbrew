// Package provider defines the LLM provider abstraction used by the AI
// agent runtime. Concrete drivers (openai, anthropic, ollama) live in this
// same package so they can share the message/usage types without bouncing
// through an internal interface.
//
// The runtime only depends on LLMClient; selecting which driver backs a
// given client happens in the registry (Resolve), keyed on the stored
// ai_provider_configs row.
package provider

import (
	"context"
	"errors"
	"sort"
	"time"
)

// Direction selects the transport side. Embeddings are reserved for future
// use; v1 only exercises Chat.
type Direction string

const (
	DirectionChat  Direction = "chat"
	DirectionEmbed Direction = "embed"
)

// Role enumerates the message roles exchanged with the model.
type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

// Message is one turn in a chat conversation. ToolCalls is set only on
// assistant turns; ToolCallID/ToolName only on tool turns.
type Message struct {
	Role       Role       `json:"role"`
	Content    string     `json:"content"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	ToolName   string     `json:"tool_name,omitempty"`
}

// ToolCall captures an assistant request to invoke a tool. ID is the
// provider-issued correlation token; the runtime echoes it on the
// subsequent role=tool message.
type ToolCall struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Args string `json:"args"` // raw JSON arguments string
}

// ToolSchema is the JSON-Schema-shaped tool description sent to the model.
// Drivers translate this to the provider-specific wire format.
type ToolSchema struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
}

// Usage reports token accounting for one assistant response.
type Usage struct {
	PromptTokens     int  `json:"prompt_tokens"`
	CompletionTokens int  `json:"completion_tokens"`
	Unavailable      bool `json:"unavailable,omitempty"`
}

// Request is the input to LLMClient.ChatStream.
type Request struct {
	Model       string
	Messages    []Message
	Tools       []ToolSchema
	Temperature float32
	MaxTokens   int
}

// DeltaKind classifies a streaming token emitted by ChatStream.
type DeltaKind string

const (
	// DeltaContent is a partial assistant text fragment.
	DeltaContent DeltaKind = "content"
	// DeltaToolCallStart opens a tool call; subsequent DeltaToolCallArgs
	// fragments stream its JSON arguments. Index identifies the slot when
	// multiple calls are issued in one assistant turn.
	DeltaToolCallStart DeltaKind = "tool_call_start"
	// DeltaToolCallArgs streams the raw JSON arguments of the tool call
	// identified by Index.
	DeltaToolCallArgs DeltaKind = "tool_call_args"
	// DeltaDone signals the end of the assistant turn; Usage carries the
	// final token totals.
	DeltaDone DeltaKind = "done"
	// DeltaError signals a fatal streaming error. After emitting it the
	// driver closes the channel.
	DeltaError DeltaKind = "error"
)

// Delta is one streamed token. Kind dictates which fields are meaningful.
type Delta struct {
	Kind       DeltaKind `json:"kind"`
	Content    string    `json:"content,omitempty"`
	Index      int       `json:"index,omitempty"`
	ToolCallID string    `json:"tool_call_id,omitempty"`
	ToolName   string    `json:"tool_name,omitempty"`
	Usage      *Usage    `json:"usage,omitempty"`
	Err        error     `json:"-"`
}

// LLMClient streams a chat turn. The returned channel is closed when the
// turn ends (either cleanly after a DeltaDone, or after a DeltaError).
// Cancelling ctx aborts the upstream HTTP request promptly.
type LLMClient interface {
	ChatStream(ctx context.Context, req Request) <-chan Delta
	// Model returns the wire model id this client will send. Useful for
	// diagnostics and for the runtime to honour the agent's stored model.
	Model() string
}

func sendDelta(ctx context.Context, out chan<- Delta, d Delta) bool {
	select {
	case <-ctx.Done():
		return false
	case out <- d:
		return true
	}
}

// Config holds the values needed to build a driver. APIKey is already
// decrypted by the caller.
type Config struct {
	Direction Direction
	Driver    string
	BaseURL   string
	Model     string
	APIKey    string
	Timeout   time.Duration // per-request timeout; 0 = no override
}

// ErrUnsupportedDriver is returned by Resolve for unknown drivers.
var ErrUnsupportedDriver = errors.New("provider: unsupported driver")

// ErrInvalidConfig is returned by drivers when their Config is malformed.
var ErrInvalidConfig = errors.New("provider: invalid config")

// Driver builds an LLMClient from a Config.
type Driver interface {
	Build(cfg Config) (LLMClient, error)
	// DefaultModel returns a sensible model id when the stored row omits
	// one. It is also surfaced in the admin panel as the placeholder.
	DefaultModel() string
	// NeedsAPIKey reports whether the driver requires an API key. Local
	// runtimes like Ollama return false.
	NeedsAPIKey() bool
}

// drivers keyed by name. Drivers register themselves at init time.
var drivers = map[string]Driver{}

// Register adds a driver under name. Panics on duplicate registration so a
// startup bug fails loudly rather than silently shadowing a driver.
func Register(name string, d Driver) {
	if _, exists := drivers[name]; exists {
		panic("provider: duplicate driver " + name)
	}
	drivers[name] = d
}

// LookupDriver returns the driver registered under name.
func LookupDriver(name string) (Driver, bool) {
	d, ok := drivers[name]
	return d, ok
}

// Drivers returns the registered driver names in stable order.
func Drivers() []string {
	names := make([]string, 0, len(drivers))
	for k := range drivers {
		names = append(names, k)
	}
	sort.Strings(names)
	return names
}

// Build resolves a driver and constructs an LLMClient.
func Build(cfg Config) (LLMClient, error) {
	d, ok := drivers[cfg.Driver]
	if !ok {
		return nil, ErrUnsupportedDriver
	}
	return d.Build(cfg)
}
