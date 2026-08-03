package session

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// Repository persists sessions.
type Repository struct {
	db *sql.DB
}

// NewRepository returns a Repository bound to db.
func NewRepository(db *sql.DB) *Repository { return &Repository{db: db} }

// ErrNotFound is returned when no row matches the lookup.
var ErrNotFound = errors.New("session not found")

// Create inserts a new session row.
func (r *Repository) Create(ctx context.Context, s *Session) error {
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO sessions (id, user_id, session_token_hash, ip_address, user_agent, created_at, expires_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`, s.ID, s.UserID, s.SessionTokenHash,
		nullable(s.IPAddress), nullable(s.UserAgent),
		s.CreatedAt, s.ExpiresAt,
	)
	return err
}

// GetByTokenHash looks up a session by SHA-256 hash of its cookie token.
func (r *Repository) GetByTokenHash(ctx context.Context, hash string) (*Session, error) {
	s := &Session{}
	err := r.db.QueryRowContext(ctx, `
		SELECT id, user_id, session_token_hash,
			COALESCE(ip_address, ''), COALESCE(user_agent, ''),
			created_at, expires_at, revoked_at
		FROM sessions WHERE session_token_hash = ?
	`, hash).Scan(&s.ID, &s.UserID, &s.SessionTokenHash,
		&s.IPAddress, &s.UserAgent,
		&s.CreatedAt, &s.ExpiresAt, &s.RevokedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return s, nil
}

// Revoke marks a session as revoked at the given time.
func (r *Repository) Revoke(ctx context.Context, id string) error {
	now := time.Now().UTC().Truncate(time.Second)
	_, err := r.db.ExecContext(ctx,
		"UPDATE sessions SET revoked_at = ? WHERE id = ?", now, id,
	)
	return err
}

// RevokeAllForUserExcept revokes every active, unexpired session for userID
// other than keepSessionID. Returns the number of sessions revoked. Used after
// a credential change so other devices are forced to re-authenticate while the
// current session stays alive.
func (r *Repository) RevokeAllForUserExcept(ctx context.Context, userID, keepSessionID string) (int64, error) {
	now := time.Now().UTC().Truncate(time.Second)
	res, err := r.db.ExecContext(ctx, `
		UPDATE sessions
		SET revoked_at = ?
		WHERE user_id = ? AND id <> ?
		  AND revoked_at IS NULL AND expires_at > ?
	`, now, userID, keepSessionID, now)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}
