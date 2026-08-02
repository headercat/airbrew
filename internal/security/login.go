package security

import (
	"context"
	"time"

	"github.com/headercat/airbrew/internal/id"
)

// LoginAttempt is one recorded login try (success or failure).
type LoginAttempt struct {
	ID        string    `json:"id"`
	UserID    string    `json:"user_id"`
	Email     string    `json:"email"`
	Success   bool      `json:"success"`
	IPAddress string    `json:"ip_address"`
	UserAgent string    `json:"user_agent"`
	Failure   string    `json:"failure,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// RecordLoginAttempt writes one login attempt row. userID may be empty when the
// email did not resolve to a known account. Recording is best-effort: a
// database error is returned but the caller is expected to treat it as
// non-fatal so it never blocks authentication.
func (s *Service) RecordLoginAttempt(ctx context.Context, a LoginAttempt) error {
	if a.ID == "" {
		a.ID = id.New()
	}
	var userID any
	if a.UserID != "" {
		userID = a.UserID
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO login_attempts (id, user_id, email, success, ip_address, user_agent, failure, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	`, a.ID, userID, a.Email, a.Success, nullable(a.IPAddress), nullable(a.UserAgent),
		nullable(a.Failure), time.Now().UTC().Truncate(time.Second))
	return err
}

// LoginHistoryFilter controls the login-history query.
type LoginHistoryFilter struct {
	Email  string
	UserID string
	From   time.Time
	To     time.Time
	Only   string // "success" | "failure" | "" (both)
	Limit  int
	Offset int
}

// ListLoginAttempts returns login attempts matching f, newest first.
func (s *Service) ListLoginAttempts(ctx context.Context, f LoginHistoryFilter) ([]LoginAttempt, int, error) {
	if f.Limit <= 0 || f.Limit > 200 {
		f.Limit = 50
	}
	if f.Offset < 0 {
		f.Offset = 0
	}
	where := "WHERE 1=1"
	args := []any{}
	if f.Email != "" {
		where += " AND email = ?"
		args = append(args, f.Email)
	}
	if f.UserID != "" {
		where += " AND user_id = ?"
		args = append(args, f.UserID)
	}
	if f.Only == "success" {
		where += " AND success = 1"
	} else if f.Only == "failure" {
		where += " AND success = 0"
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
	if err := s.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM login_attempts "+where, args...,
	).Scan(&total); err != nil {
		return nil, 0, err
	}

	query := `SELECT id, COALESCE(user_id,''), email, success,
			COALESCE(ip_address,''), COALESCE(user_agent,''), COALESCE(failure,''), created_at
		FROM login_attempts ` + where + `
		ORDER BY created_at DESC LIMIT ? OFFSET ?`
	args = append(args, f.Limit, f.Offset)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []LoginAttempt
	for rows.Next() {
		var a LoginAttempt
		var success int
		if err := rows.Scan(&a.ID, &a.UserID, &a.Email, &success,
			&a.IPAddress, &a.UserAgent, &a.Failure, &a.CreatedAt); err != nil {
			return nil, 0, err
		}
		a.Success = success == 1
		out = append(out, a)
	}
	return out, total, rows.Err()
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}
