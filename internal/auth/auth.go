// Package auth wires together the user, session and OAuth sub-modules.
//
// Milestone 1 (this file) covers user/password/session.
// OAuth 2.1 endpoints (authorize/token/jwks/discovery/refresh) are stubbed
// under internal/auth/oauth and will be wired here in later milestones.
package auth

import (
	"database/sql"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/headercat/airbrew/internal/audit"
	"github.com/headercat/airbrew/internal/auth/handler"
	"github.com/headercat/airbrew/internal/auth/session"
	"github.com/headercat/airbrew/internal/auth/user"
	"github.com/headercat/airbrew/internal/blob"
	"github.com/headercat/airbrew/internal/security"
)

// Config carries auth-module tuning knobs.
type Config struct {
	SessionMaxAge time.Duration
	Blobs         blob.Store
	Audit         *audit.Service
	Security      *security.Service
	// CookieSecure marks the session cookie Secure (HTTPS-only). Required for
	// production deployments; defaults to false for local HTTP dev.
	CookieSecure bool
}

// Module bundles the user/session/OAuth services and HTTP handlers.
type Module struct {
	UserRepo *user.Repository
	UserSvc  *user.Service
	SessRepo *session.Repository
	SessSvc  *session.Service
	Security *security.Service
	Handler  *handler.Handler
}

// New builds the auth Module bound to the given database.
func New(db *sql.DB, cfg Config) *Module {
	userRepo := user.NewRepository(db)
	userSvc := user.NewService(userRepo)
	if cfg.Security != nil {
		userSvc.SetPasswordPolicyChecker(cfg.Security)
	}
	sessRepo := session.NewRepository(db)
	sessSvc := session.NewService(sessRepo, cfg.SessionMaxAge)
	h := handler.New(handler.Deps{
		UserRepo:     userRepo,
		UserSvc:      userSvc,
		SessSvc:      sessSvc,
		Blobs:        cfg.Blobs,
		Audit:        cfg.Audit,
		Security:     cfg.Security,
		CookieSecure: cfg.CookieSecure,
	})
	return &Module{
		UserRepo: userRepo,
		UserSvc:  userSvc,
		SessRepo: sessRepo,
		SessSvc:  sessSvc,
		Security: cfg.Security,
		Handler:  h,
	}
}

// SessionMiddleware loads any active session into the request context.
// It is best-effort: missing/invalid/expired cookies are silently ignored
// so the rest of the request can decide how to react.
func (m *Module) SessionMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !m.allowIP(w, r) {
			return
		}
		c, err := r.Cookie(session.CookieName)
		if err != nil || c.Value == "" {
			next.ServeHTTP(w, r)
			return
		}
		sess, err := m.SessSvc.Lookup(r.Context(), c.Value)
		if err != nil {
			next.ServeHTTP(w, r)
			return
		}
		ctx := session.WithContext(r.Context(), sess)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (m *Module) allowIP(w http.ResponseWriter, r *http.Request) bool {
	if m.Security == nil {
		return true
	}
	ok, err := m.Security.AllowsIP(r.Context(), requestIP(r))
	if err != nil {
		deny(w, http.StatusInternalServerError, "internal_error", err.Error())
		return false
	}
	if !ok {
		deny(w, http.StatusForbidden, "ip_not_allowed", "request IP is not allowed")
		return false
	}
	return true
}

func requestIP(r *http.Request) string {
	if f := r.Header.Get("X-Forwarded-For"); f != "" {
		parts := strings.SplitN(f, ",", 2)
		return strings.TrimSpace(parts[0])
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil {
		return host
	}
	return r.RemoteAddr
}

func deny(w http.ResponseWriter, status int, code, desc string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(`{"error":"` + code + `","error_description":"` + desc + `"}`))
}

// RegisterRoutes mounts all auth-module routes on mux, wrapped in the
// session middleware so /api/auth/me can read the loaded session.
func (m *Module) RegisterRoutes(mux *http.ServeMux) {
	m.Handler.RegisterRoutes(mux)
}
