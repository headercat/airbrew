// Package middleware contains common HTTP middleware for the server.
package middleware

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/http"
	"runtime/debug"
	"strings"
	"time"
)

type ctxKey int

const reqIDKey ctxKey = 0

// Chain wraps h with the given middlewares in declaration order (first runs first).
func Chain(h http.Handler, mws ...func(http.Handler) http.Handler) http.Handler {
	for i := len(mws) - 1; i >= 0; i-- {
		h = mws[i](h)
	}
	return h
}

// RequestID ensures every request has an X-Request-ID (echoed on response).
func RequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Request-ID")
		if id == "" {
			b := make([]byte, 8)
			_, _ = rand.Read(b)
			id = hex.EncodeToString(b)
		}
		w.Header().Set("X-Request-ID", id)
		ctx := context.WithValue(r.Context(), reqIDKey, id)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// Recover catches panics and logs them with a stack trace.
func Recover(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if rec := recover(); rec != nil {
					logger.Error("panic recovered",
						"panic", rec,
						"stack", string(debug.Stack()),
						"path", r.URL.Path,
					)
					http.Error(w, `{"error":"internal_server_error"}`, http.StatusInternalServerError)
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}

type statusWriter struct {
	http.ResponseWriter
	status    int
	bytes     int
	errorBody strings.Builder
}

func (w *statusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	if w.status >= 400 && w.errorBody.Len() < 1024 {
		remaining := 1024 - w.errorBody.Len()
		if len(b) > remaining {
			b = b[:remaining]
		}
		_, _ = w.errorBody.Write(b)
	}
	n, err := w.ResponseWriter.Write(b)
	w.bytes += n
	return n, err
}

// AccessLog logs one human-readable line per HTTP request.
func AccessLog(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			sw := &statusWriter{ResponseWriter: w}
			next.ServeHTTP(sw, r)
			status := sw.status
			if status == 0 {
				status = http.StatusOK
			}
			duration := time.Since(start)
			msg := fmt.Sprintf("%s %s -> %d %s (%s)", r.Method, r.URL.RequestURI(), status, http.StatusText(status), duration.Round(time.Millisecond))
			attrs := []any{
				"method", r.Method,
				"path", r.URL.RequestURI(),
				"status", status,
				"bytes", sw.bytes,
				"duration", duration.Round(time.Millisecond).String(),
				"ip", r.RemoteAddr,
				"request_id", RequestIDFromContext(r.Context()),
			}
			if status >= 400 {
				if body := strings.TrimSpace(sw.errorBody.String()); body != "" {
					attrs = append(attrs, "error_response", body)
				}
			}
			switch {
			case status >= 500:
				logger.Error(msg, attrs...)
			case status >= 400:
				logger.Warn(msg, attrs...)
			default:
				logger.Info(msg, attrs...)
			}
		})
	}
}

// RequestIDFromContext returns the X-Request-ID stored by RequestID, if any.
func RequestIDFromContext(ctx context.Context) string {
	v, _ := ctx.Value(reqIDKey).(string)
	return v
}
