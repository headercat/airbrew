// Package conv holds the conversation and message domain for the AI module.
//
// A Conversation is owned by one user and bound to one agent snapshot at
// creation time. Messages are appended in monotonic seq order. Tool turns
// (role=tool) carry the originating ToolCallID so the runtime can replay
// history deterministically across providers.
package conv

import (
	"errors"
	"time"

	"github.com/headercat/airbrew/internal/ai/provider"
)

// Sentinel errors returned by the repository and service.
var (
	ErrNotFound     = errors.New("conv: not found")
	ErrInvalidInput = errors.New("conv: invalid input")
)

// Conversation is one chat thread.
type Conversation struct {
	ID        string
	UserID    string
	AgentID   string
	Title     string
	Revision  int64
	CreatedAt time.Time
	UpdatedAt time.Time
	DeletedAt *time.Time

	// Snapshot of the agent at conversation creation. Mutating the agent
	// later does not change in-flight conversations.
	SnapModel       string
	SnapSystem      string
	SnapTools       []string
	SnapTemperature float32
	SnapMaxTokens   int
	SnapMaxTurns    int
}

// Message is one ordered turn within a conversation.
type Message struct {
	ID               string
	ConversationID   string
	Role             provider.Role
	Content          string
	ToolCalls        []provider.ToolCall
	ToolCallID       string
	ToolName         string
	PromptTokens     int
	CompletionTokens int
	Seq              int
	CreatedAt        time.Time
}

// Snapshot is the API-facing projection of a Conversation (no internal
// fields). Returned by the handler for the SPA.
type Snapshot struct {
	ID        string            `json:"id"`
	AgentID   string            `json:"agent_id"`
	Title     string            `json:"title"`
	Model     string            `json:"model"`
	Revision  int64             `json:"revision"`
	CreatedAt string            `json:"created_at"`
	UpdatedAt string            `json:"updated_at"`
	DeletedAt *string           `json:"deleted_at,omitempty"`
	System    string            `json:"system,omitempty"`
	Tools     []string          `json:"tools,omitempty"`
}

// Input constraints. They are enforced in the service to bound DB row
// size and prompt cost.
const (
	MaxTitleLen     = 200
	MaxContentLen   = 32 * 1024
	MaxHistoryLimit = 100
	MaxSystemLen    = 8 * 1024
	MaxTools        = 32
)
