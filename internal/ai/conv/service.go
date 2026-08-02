// Package conv — service.go
//
// Business logic for conversations and messages. The service validates
// input shapes, enforces length caps, and forwards to the repository. It
// is intentionally small: tool dispatch and provider I/O live in the
// agent runtime.
package conv

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/headercat/airbrew/internal/ai/provider"
)

// Service contains conversation business logic.
type Service struct {
	repo *Repository
}

// NewService returns a Service backed by repo.
func NewService(repo *Repository) *Service { return &Service{repo: repo} }

// CreateInput is the validated create payload.
type CreateInput struct {
	UserID  string
	AgentID string
	Title   string

	SnapModel       string
	SnapSystem      string
	SnapTools       []string
	SnapTemperature float32
	SnapMaxTokens   int
	SnapMaxTurns    int
}

// Create inserts a new conversation with the agent snapshot baked in.
func (s *Service) Create(ctx context.Context, in CreateInput) (Conversation, error) {
	if in.UserID == "" {
		return Conversation{}, fmt.Errorf("%w: user_id required", ErrInvalidInput)
	}
	if in.AgentID == "" {
		return Conversation{}, fmt.Errorf("%w: agent_id required", ErrInvalidInput)
	}
	if in.SnapModel == "" {
		return Conversation{}, fmt.Errorf("%w: snap_model required", ErrInvalidInput)
	}
	if len(in.SnapSystem) > MaxSystemLen {
		return Conversation{}, fmt.Errorf("%w: system prompt too long", ErrInvalidInput)
	}
	if len(in.SnapTools) > MaxTools {
		return Conversation{}, fmt.Errorf("%w: too many tools", ErrInvalidInput)
	}
	if in.SnapMaxTurns <= 0 || in.SnapMaxTurns > 20 {
		in.SnapMaxTurns = 6
	}
	title := strings.TrimSpace(in.Title)
	if len(title) > MaxTitleLen {
		title = title[:MaxTitleLen]
	}
	return s.repo.Create(ctx, Conversation{
		UserID: in.UserID, AgentID: in.AgentID, Title: title,
		SnapModel: in.SnapModel, SnapSystem: in.SnapSystem, SnapTools: in.SnapTools,
		SnapTemperature: in.SnapTemperature, SnapMaxTokens: in.SnapMaxTokens,
		SnapMaxTurns: in.SnapMaxTurns,
	})
}

// Get returns one conversation (ownership-scoped).
func (s *Service) Get(ctx context.Context, userID, id string) (Conversation, []Message, error) {
	c, err := s.repo.Get(ctx, userID, id)
	if err != nil {
		return Conversation{}, nil, err
	}
	msgs, err := s.repo.ListMessages(ctx, userID, id)
	if err != nil {
		return Conversation{}, nil, err
	}
	return c, msgs, nil
}

// List returns conversations for the user.
func (s *Service) List(ctx context.Context, userID string, limit, offset int) ([]Conversation, error) {
	return s.repo.List(ctx, userID, limit, offset)
}

// Delete soft-deletes a conversation.
func (s *Service) Delete(ctx context.Context, userID, id string) error {
	return s.repo.SoftDelete(ctx, userID, id)
}

// SetTitle renames a conversation (ownership-scoped).
func (s *Service) SetTitle(ctx context.Context, userID, id, title string) error {
	return s.repo.SetTitle(ctx, userID, id, strings.TrimSpace(title))
}

// SetTitleIfEmpty renames a conversation only if it has no title yet.
func (s *Service) SetTitleIfEmpty(ctx context.Context, userID, id, title string) (bool, error) {
	return s.repo.SetTitleIfEmpty(ctx, userID, id, strings.TrimSpace(title))
}

// AcquireRunLease prevents overlapping assistant runs for a conversation.
func (s *Service) AcquireRunLease(ctx context.Context, userID, conversationID, runID string, ttl time.Duration) error {
	return s.repo.AcquireRunLease(ctx, userID, conversationID, runID, ttl)
}

// ReleaseRunLease releases a previously acquired run lease.
func (s *Service) ReleaseRunLease(ctx context.Context, conversationID, runID string) error {
	return s.repo.ReleaseRunLease(ctx, conversationID, runID)
}

// AppendUserMessage records a user turn and returns the inserted row.
func (s *Service) AppendUserMessage(ctx context.Context, userID, conversationID, content string) (Message, error) {
	return s.AppendUserMessageIfRevision(ctx, userID, conversationID, content, 0)
}

// AppendUserMessageIfRevision records a user turn only if the conversation
// still has expectedRevision. expectedRevision <= 0 disables the check.
func (s *Service) AppendUserMessageIfRevision(ctx context.Context, userID, conversationID, content string, expectedRevision int64) (Message, error) {
	content = strings.TrimSpace(content)
	if content == "" {
		return Message{}, fmt.Errorf("%w: content required", ErrInvalidInput)
	}
	if len(content) > MaxContentLen {
		return Message{}, fmt.Errorf("%w: content too long", ErrInvalidInput)
	}
	m := Message{
		ConversationID: conversationID, Role: provider.RoleUser, Content: content,
	}
	if expectedRevision > 0 {
		return s.repo.AppendMessageIfRevision(ctx, userID, m, expectedRevision)
	}
	return s.repo.AppendMessage(ctx, userID, m)
}

// AppendAssistantMessage records an assistant turn (with tool calls and
// usage) and rolls the tokens into the per-user daily counter.
func (s *Service) AppendAssistantMessage(ctx context.Context, userID, conversationID, content string,
	toolCalls []provider.ToolCall, promptTok, completionTok int,
) (Message, error) {
	m, err := s.repo.AppendMessage(ctx, userID, Message{
		ConversationID: conversationID, Role: provider.RoleAssistant, Content: content,
		ToolCalls: toolCalls, PromptTokens: promptTok, CompletionTokens: completionTok,
	})
	if err != nil {
		return Message{}, err
	}
	if err := s.repo.IncUsage(ctx, userID, time.Now(), promptTok, completionTok); err != nil {
		// Usage rollup is best-effort; never fail the request on it.
		slog.Default().Warn("ai: usage rollup failed", "user_id", userID, "conversation_id", conversationID, "error", err)
	}
	return m, nil
}

// AppendToolMessage records a tool result turn.
func (s *Service) AppendToolMessage(ctx context.Context, userID, conversationID, toolCallID, toolName, content string) (Message, error) {
	if toolCallID == "" {
		return Message{}, fmt.Errorf("%w: tool_call_id required", ErrInvalidInput)
	}
	if len(content) > MaxContentLen {
		return Message{}, fmt.Errorf("%w: tool content too long", ErrInvalidInput)
	}
	return s.repo.AppendMessage(ctx, userID, Message{
		ConversationID: conversationID, Role: provider.RoleTool,
		Content: content, ToolCallID: toolCallID, ToolName: toolName,
	})
}

// History loads the prior turns of a conversation as provider Messages.
// Tool turns are emitted in seq order; the runtime prepends a system
// message itself from the conversation snapshot.
func (s *Service) History(ctx context.Context, userID, conversationID string) ([]provider.Message, error) {
	msgs, err := s.repo.ListMessages(ctx, userID, conversationID)
	if err != nil {
		return nil, err
	}
	out := make([]provider.Message, 0, len(msgs))
	for _, m := range msgs {
		pm := provider.Message{
			Role: m.Role, Content: m.Content,
			ToolCalls: m.ToolCalls, ToolCallID: m.ToolCallID, ToolName: m.ToolName,
		}
		out = append(out, pm)
	}
	return out, nil
}

// Usage returns daily token rollups since the given UTC day. Empty userID
// aggregates across all users.
func (s *Service) Usage(ctx context.Context, userID string, since time.Time) ([]UsageDay, error) {
	return s.repo.ListUsage(ctx, strings.TrimSpace(userID), since)
}
