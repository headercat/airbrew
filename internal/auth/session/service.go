package session

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"time"

	"github.com/headercat/airbrew/internal/id"
)

// Service issues, looks up and revokes sessions.
type Service struct {
	repo   *Repository
	maxAge time.Duration
}

// NewService returns a Service backed by repo with the given session lifetime.
func NewService(repo *Repository, maxAge time.Duration) *Service {
	return &Service{repo: repo, maxAge: maxAge}
}

// Issue creates a new session for userID and returns the raw cookie token.
func (s *Service) Issue(ctx context.Context, userID, ip, ua string) (token string, sess *Session, err error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", nil, err
	}
	token = base64.RawURLEncoding.EncodeToString(raw)
	now := time.Now().UTC().Truncate(time.Second)
	sess = &Session{
		ID:               id.New(),
		UserID:           userID,
		SessionTokenHash: hashToken(token),
		IPAddress:        ip,
		UserAgent:        ua,
		CreatedAt:        now,
		ExpiresAt:        now.Add(s.maxAge),
	}
	if err := s.repo.Create(ctx, sess); err != nil {
		return "", nil, err
	}
	return token, sess, nil
}

// Lookup returns the session for a raw cookie token, validating expiry/revocation.
func (s *Service) Lookup(ctx context.Context, token string) (*Session, error) {
	if token == "" {
		return nil, ErrNotFound
	}
	sess, err := s.repo.GetByTokenHash(ctx, hashToken(token))
	if err != nil {
		return nil, err
	}
	if sess.RevokedAt != nil {
		return nil, errors.New("session revoked")
	}
	if time.Now().After(sess.ExpiresAt) {
		return nil, errors.New("session expired")
	}
	return sess, nil
}

// Revoke marks the session as revoked.
func (s *Service) Revoke(ctx context.Context, sessID string) error {
	return s.repo.Revoke(ctx, sessID)
}

// RevokeAllForUserExcept revokes every active session for userID except the one
// named by keepSessionID, returning how many were revoked.
func (s *Service) RevokeAllForUserExcept(ctx context.Context, userID, keepSessionID string) (int64, error) {
	return s.repo.RevokeAllForUserExcept(ctx, userID, keepSessionID)
}

// MaxAge returns the configured session lifetime (seconds), for cookie Max-Age.
func (s *Service) MaxAge() int { return int(s.maxAge / time.Second) }

func hashToken(token string) string {
	h := sha256.Sum256([]byte(token))
	return base64.RawURLEncoding.EncodeToString(h[:])
}
