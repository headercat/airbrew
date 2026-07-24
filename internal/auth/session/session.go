// Package session implements browser login sessions.
//
// A cookie value is opaque random bytes; only its SHA-256 hash is persisted.
// Sessions are looked up by hash so a DB leak cannot hijack live sessions.
package session

import (
	"context"
	"time"
)

// Session is a browser login session record.
type Session struct {
	ID               string
	UserID           string
	SessionTokenHash string
	IPAddress        string
	UserAgent        string
	CreatedAt        time.Time
	ExpiresAt        time.Time
	RevokedAt        *time.Time
}

type ctxKey struct{}

// CookieName is the HTTP cookie name carrying the opaque session token.
const CookieName = "airbrew_session"

// WithContext attaches s to ctx so downstream handlers can read it.
func WithContext(ctx context.Context, s *Session) context.Context {
	return context.WithValue(ctx, ctxKey{}, s)
}

// FromContext returns the session attached by the session middleware.
func FromContext(ctx context.Context) (*Session, bool) {
	s, ok := ctx.Value(ctxKey{}).(*Session)
	return s, ok
}
