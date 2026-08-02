// Package middleware — ratelimit.go
//
// A minimal in-memory fixed-window rate limiter. It is deliberately
// dependency-free and process-local: Airbrew is single-tenant and single-
// process, so a per-IP in-process counter is the right granularity for
// defending against abusive clients (resource exhaustion on the vault
// envelope/setup endpoints, future server-side-secret probes, etc.). It is not
// a substitute for argon2id's offline-brute-force resistance — the vault key
// never leaves the client — but caps the blast radius of a misbehaving session.
package middleware

import (
	"net/http"
	"sync"
	"time"

	"github.com/headercat/airbrew/internal/httpserver/requestip"
)

// RateLimiter is a fixed-window per-key limiter. The zero value is not usable;
// use NewRateLimiter.
type RateLimiter struct {
	mu     sync.Mutex
	count  map[string]*bucket
	limit  int
	window time.Duration
}

type bucket struct {
	count   int
	resetAt time.Time
}

// NewRateLimiter returns a limiter that allows at most limit requests per key
// in every window. Keys are opaque strings (typically a client IP or user ID).
func NewRateLimiter(limit int, window time.Duration) *RateLimiter {
	if limit <= 0 {
		limit = 1
	}
	if window <= 0 {
		window = time.Minute
	}
	return &RateLimiter{count: make(map[string]*bucket), limit: limit, window: window}
}

// Allow reports whether key may proceed, consuming one token if so.
func (r *RateLimiter) Allow(key string) bool {
	now := time.Now()
	r.mu.Lock()
	defer r.mu.Unlock()
	b, ok := r.count[key]
	if !ok || now.After(b.resetAt) {
		r.count[key] = &bucket{count: 1, resetAt: now.Add(r.window)}
		// Opportunistic GC of expired entries to bound memory.
		if len(r.count) > 4096 {
			for k, v := range r.count {
				if now.After(v.resetAt) {
					delete(r.count, k)
				}
			}
		}
		return true
	}
	if b.count >= r.limit {
		return false
	}
	b.count++
	return true
}

// RateLimit returns middleware that throttles requests using key as the bucket
// key (typically the client IP). Requests over the limit get 429.
func RateLimit(limiter *RateLimiter, key func(*http.Request) string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !limiter.Allow(key(r)) {
				w.Header().Set("Retry-After", "60")
				http.Error(w, `{"error":"rate_limited","error_description":"too many requests"}`,
					http.StatusTooManyRequests)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// ClientIPKey extracts a stable direct client-IP bucket key from a request.
func ClientIPKey(r *http.Request) string {
	return requestip.DirectClientIP(r)
}
