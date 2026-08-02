// Package conv — repository.go
//
// SQL persistence for ai_conversations and ai_messages. Conversations are
// ownership-scoped (every query joins on user_id); a monotonic per-user
// revision counter on ai_conversations drives multi-tab sync.
package conv

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/headercat/airbrew/internal/ai/provider"
	"github.com/headercat/airbrew/internal/id"
)

// Repository persists conversations and messages.
type Repository struct {
	db *sql.DB
}

// NewRepository returns a Repository bound to db.
func NewRepository(db *sql.DB) *Repository { return &Repository{db: db} }

const convColumns = `id, user_id, agent_id, title, revision, created_at, updated_at, deleted_at,
snap_model, snap_system, snap_tools, snap_temperature, snap_max_tokens, snap_max_turns`

// Create inserts a conversation with the agent snapshot baked in.
func (r *Repository) Create(ctx context.Context, c Conversation) (Conversation, error) {
	if c.UserID == "" {
		return Conversation{}, fmt.Errorf("%w: user_id required", ErrInvalidInput)
	}
	if c.AgentID == "" {
		return Conversation{}, fmt.Errorf("%w: agent_id required", ErrInvalidInput)
	}
	tools := "[]"
	if c.SnapTools != nil {
		b, err := json.Marshal(c.SnapTools)
		if err != nil {
			return Conversation{}, fmt.Errorf("%w: marshal tools: %v", ErrInvalidInput, err)
		}
		tools = string(b)
	}
	now := time.Now().UTC().Truncate(time.Second)
	c.ID = id.New()
	c.CreatedAt = now
	c.UpdatedAt = now
	c.Revision = 1
	if c.SnapTools == nil {
		c.SnapTools = []string{}
	}
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO ai_conversations
		  (id, user_id, agent_id, title, revision, created_at, updated_at, deleted_at,
		   snap_model, snap_system, snap_tools, snap_temperature, snap_max_tokens, snap_max_turns)
		VALUES (?, ?, ?, ?, ?, ?, ?, NULL, ?, ?, ?, ?, ?, ?)
	`, c.ID, c.UserID, c.AgentID, c.Title, c.Revision, c.CreatedAt, c.UpdatedAt,
		c.SnapModel, c.SnapSystem, tools, c.SnapTemperature, c.SnapMaxTokens, c.SnapMaxTurns)
	if err != nil {
		return Conversation{}, fmt.Errorf("conv: create: %w", err)
	}
	return c, nil
}

// Get returns one conversation, ownership-scoped.
func (r *Repository) Get(ctx context.Context, userID, id string) (Conversation, error) {
	row := r.db.QueryRowContext(ctx,
		"SELECT "+convColumns+" FROM ai_conversations WHERE id = ? AND user_id = ?",
		id, userID)
	c, err := scanConv(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Conversation{}, ErrNotFound
	}
	return c, err
}

// List returns conversations for the user (excluding soft-deleted),
// newest-updated first.
func (r *Repository) List(ctx context.Context, userID string, limit, offset int) ([]Conversation, error) {
	if limit <= 0 || limit > MaxHistoryLimit {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	rows, err := r.db.QueryContext(ctx, `
		SELECT `+convColumns+` FROM ai_conversations
		WHERE user_id = ? AND deleted_at IS NULL
		ORDER BY updated_at DESC
		LIMIT ? OFFSET ?`, userID, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Conversation
	for rows.Next() {
		c, err := scanConv(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// SoftDelete marks a conversation as deleted and bumps revision.
func (r *Repository) SoftDelete(ctx context.Context, userID, id string) error {
	now := time.Now().UTC().Truncate(time.Second)
	res, err := r.db.ExecContext(ctx, `
		UPDATE ai_conversations
		SET deleted_at = ?, updated_at = ?, revision = revision + 1
		WHERE id = ? AND user_id = ? AND deleted_at IS NULL`,
		now, now, id, userID)
	if err != nil {
		return fmt.Errorf("conv: soft-delete: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// BumpRevision increments the per-user sync cursor. Callers invoke it after
// appending new messages so a separate poll can detect the change.
func (r *Repository) BumpRevision(ctx context.Context, userID, id string) error {
	_, err := r.db.ExecContext(ctx,
		"UPDATE ai_conversations SET revision = revision + 1 WHERE id = ? AND user_id = ?",
		id, userID)
	return err
}

// AppendMessage inserts a message with seq assigned inside the transaction
// so order is always deterministic. Returns the inserted row.
func (r *Repository) AppendMessage(ctx context.Context, userID string, m Message) (Message, error) {
	if userID == "" {
		return Message{}, fmt.Errorf("%w: user_id required", ErrInvalidInput)
	}
	if m.ConversationID == "" {
		return Message{}, fmt.Errorf("%w: conversation_id required", ErrInvalidInput)
	}
	// Ownership guard: ensure the conversation belongs to userID first.
	var owner string
	err := r.db.QueryRowContext(ctx,
		"SELECT user_id FROM ai_conversations WHERE id = ?", m.ConversationID,
	).Scan(&owner)
	if errors.Is(err, sql.ErrNoRows) {
		return Message{}, ErrNotFound
	}
	if err != nil {
		return Message{}, err
	}
	if owner != userID {
		return Message{}, ErrNotFound
	}

	tools := "[]"
	if m.ToolCalls != nil {
		b, err := json.Marshal(m.ToolCalls)
		if err != nil {
			return Message{}, fmt.Errorf("%w: marshal tool_calls: %v", ErrInvalidInput, err)
		}
		tools = string(b)
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return Message{}, fmt.Errorf("conv: begin tx: %w", err)
	}
	defer tx.Rollback()

	var seq int
	if err := tx.QueryRowContext(ctx,
		"SELECT COALESCE(MAX(seq),0) + 1 FROM ai_messages WHERE conversation_id = ?",
		m.ConversationID,
	).Scan(&seq); err != nil {
		return Message{}, fmt.Errorf("conv: next seq: %w", err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	m.ID = id.New()
	m.Seq = seq
	m.CreatedAt = now
	_, err = tx.ExecContext(ctx, `
		INSERT INTO ai_messages
		  (id, conversation_id, role, content, tool_calls, tool_call_id, tool_name,
		   prompt_tokens, completion_tokens, seq, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, m.ID, m.ConversationID, string(m.Role), m.Content, tools,
		m.ToolCallID, m.ToolName, m.PromptTokens, m.CompletionTokens, m.Seq, m.CreatedAt)
	if err != nil {
		return Message{}, fmt.Errorf("conv: insert message: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		"UPDATE ai_conversations SET updated_at = ?, revision = revision + 1 WHERE id = ?",
		now, m.ConversationID,
	); err != nil {
		return Message{}, fmt.Errorf("conv: bump updated_at: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Message{}, fmt.Errorf("conv: commit: %w", err)
	}
	return m, nil
}

// ListMessages returns all messages for a conversation in seq order.
func (r *Repository) ListMessages(ctx context.Context, userID, conversationID string) ([]Message, error) {
	// Ownership guard.
	var owner string
	err := r.db.QueryRowContext(ctx,
		"SELECT user_id FROM ai_conversations WHERE id = ?", conversationID,
	).Scan(&owner)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if owner != userID {
		return nil, ErrNotFound
	}
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, conversation_id, role, content, tool_calls, tool_call_id, tool_name,
		       prompt_tokens, completion_tokens, seq, created_at
		FROM ai_messages WHERE conversation_id = ? ORDER BY seq`, conversationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Message
	for rows.Next() {
		m, err := scanMessage(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// IncUsage rolls up one assistant message's tokens into the daily counter.
func (r *Repository) IncUsage(ctx context.Context, userID string, day time.Time, prompt, completion int) error {
	if prompt < 0 {
		prompt = 0
	}
	if completion < 0 {
		completion = 0
	}
	dayStr := day.UTC().Format("2006-01-02")
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO ai_usage_daily (user_id, day, prompt_tokens, completion_tokens, request_count)
		VALUES (?, ?, ?, ?, 1)
		ON CONFLICT(user_id, day) DO UPDATE SET
		  prompt_tokens     = prompt_tokens + excluded.prompt_tokens,
		  completion_tokens = completion_tokens + excluded.completion_tokens,
		  request_count     = request_count + 1
	`, userID, dayStr, prompt, completion)
	if err != nil {
		return fmt.Errorf("conv: inc usage: %w", err)
	}
	return err
}

// --- scanners --------------------------------------------------------------

type scanner interface {
	Scan(dest ...any) error
}

func scanConv(row scanner) (Conversation, error) {
	var c Conversation
	var toolsJSON string
	var deleted sql.NullTime
	if err := row.Scan(
		&c.ID, &c.UserID, &c.AgentID, &c.Title, &c.Revision,
		&c.CreatedAt, &c.UpdatedAt, &deleted,
		&c.SnapModel, &c.SnapSystem, &toolsJSON,
		&c.SnapTemperature, &c.SnapMaxTokens, &c.SnapMaxTurns,
	); err != nil {
		return Conversation{}, err
	}
	if deleted.Valid {
		t := deleted.Time
		c.DeletedAt = &t
	}
	if toolsJSON != "" {
		_ = json.Unmarshal([]byte(toolsJSON), &c.SnapTools)
	}
	if c.SnapTools == nil {
		c.SnapTools = []string{}
	}
	return c, nil
}

func scanMessage(row scanner) (Message, error) {
	var m Message
	var role, toolsJSON string
	if err := row.Scan(
		&m.ID, &m.ConversationID, &role, &m.Content, &toolsJSON,
		&m.ToolCallID, &m.ToolName,
		&m.PromptTokens, &m.CompletionTokens, &m.Seq, &m.CreatedAt,
	); err != nil {
		return Message{}, err
	}
	m.Role = provider.Role(role)
	if toolsJSON != "" && toolsJSON != "[]" {
		_ = json.Unmarshal([]byte(toolsJSON), &m.ToolCalls)
	}
	return m, nil
}
