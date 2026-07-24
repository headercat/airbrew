// Package audit provides an append-only audit log service that records
// significant state-changing actions across all modules.
//
// Every entry captures who did what, to which target, from where, and with
// what additional context. The log is the single source of truth for admin
// visibility and compliance.
package audit

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"github.com/headercat/airbrew/internal/id"
)

// Entry describes one audit-worthy event.
type Entry struct {
	EventType     string         // e.g. "user.created", "module.disabled"
	ActorUserID   string         // internal user ID; empty for system
	ActorClientID string         // OAuth client ID; empty for browser
	TargetType    string         // "user", "module", "session", "oauth_client"
	TargetID      string         // ID of the target resource
	IPAddress     string
	UserAgent     string
	Metadata      map[string]any // additional structured context
}

// Service writes entries to the audit_logs table.
type Service struct {
	db *sql.DB
}

// NewService returns a Service bound to db.
func NewService(db *sql.DB) *Service { return &Service{db: db} }

// Log persists one entry. It never returns an error that would fail the
// caller's transaction — audit logging is best-effort by design so that a
// logging failure cannot block a user-facing operation.
func (s *Service) Log(ctx context.Context, e Entry) {
	if s == nil {
		return
	}
	meta := "{}"
	if e.Metadata != nil {
		if b, err := json.Marshal(e.Metadata); err == nil {
			meta = string(b)
		}
	}
	now := time.Now().UTC().Truncate(time.Second)
	_, _ = s.db.ExecContext(ctx, `
		INSERT INTO audit_logs
		  (id, actor_user_id, actor_client_id, event_type, target_type, target_id,
		   ip_address, user_agent, metadata, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		id.New(),
		nullable(e.ActorUserID),
		nullable(e.ActorClientID),
		e.EventType,
		nullable(e.TargetType),
		nullable(e.TargetID),
		nullable(e.IPAddress),
		nullable(e.UserAgent),
		meta,
		now,
	)
}

// ListFilter controls which entries are returned.
type ListFilter struct {
	EventType string
	ActorID   string
	TargetType string
	TargetID  string
	From      time.Time
	To        time.Time
	Limit     int
	Offset    int
}

// LogEntry is a row read back from audit_logs.
type LogEntry struct {
	ID            string    `json:"id"`
	ActorUserID   string    `json:"actor_user_id"`
	ActorClientID string    `json:"actor_client_id"`
	ActorEmail    string    `json:"actor_email,omitempty"`
	EventType     string    `json:"event_type"`
	TargetType    string    `json:"target_type"`
	TargetID      string    `json:"target_id"`
	IPAddress     string    `json:"ip_address"`
	UserAgent     string    `json:"user_agent"`
	Metadata      string    `json:"metadata"`
	CreatedAt     time.Time `json:"created_at"`
}

// List returns audit entries matching the filter, newest first.
func (s *Service) List(ctx context.Context, f ListFilter) ([]LogEntry, int, error) {
	if f.Limit <= 0 || f.Limit > 200 {
		f.Limit = 50
	}
	if f.Offset < 0 {
		f.Offset = 0
	}

	where := "WHERE 1=1"
	args := []any{}
	if f.EventType != "" {
		where += " AND event_type = ?"
		args = append(args, f.EventType)
	}
	if f.ActorID != "" {
		where += " AND actor_user_id = ?"
		args = append(args, f.ActorID)
	}
	if f.TargetType != "" {
		where += " AND target_type = ?"
		args = append(args, f.TargetType)
	}
	if f.TargetID != "" {
		where += " AND target_id = ?"
		args = append(args, f.TargetID)
	}
	if !f.From.IsZero() {
		where += " AND created_at >= ?"
		args = append(args, f.From)
	}
	if !f.To.IsZero() {
		where += " AND created_at <= ?"
		args = append(args, f.To)
	}

	var total int
	countErr := s.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM audit_logs "+where, args...,
	).Scan(&total)
	if countErr != nil {
		return nil, 0, countErr
	}

	query := `
		SELECT a.id, COALESCE(a.actor_user_id,''), COALESCE(a.actor_client_id,''),
		       COALESCE(u.email,''), a.event_type,
		       COALESCE(a.target_type,''), COALESCE(a.target_id,''),
		       COALESCE(a.ip_address,''), COALESCE(a.user_agent,''),
		       COALESCE(a.metadata,'{}'), a.created_at
		FROM audit_logs a
		LEFT JOIN users u ON u.id = a.actor_user_id
	` + where + `
	ORDER BY a.created_at DESC
	LIMIT ? OFFSET ?`
	args = append(args, f.Limit, f.Offset)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var out []LogEntry
	for rows.Next() {
		var le LogEntry
		if err := rows.Scan(
			&le.ID, &le.ActorUserID, &le.ActorClientID,
			&le.ActorEmail, &le.EventType,
			&le.TargetType, &le.TargetID,
			&le.IPAddress, &le.UserAgent,
			&le.Metadata, &le.CreatedAt,
		); err != nil {
			return nil, 0, err
		}
		out = append(out, le)
	}
	return out, total, rows.Err()
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}
