// Package auth wires together the user, session and OAuth sub-modules.
//
// Milestone 1 (this file) covers user/password/session.
// OAuth 2.1 endpoints (authorize/token/jwks/discovery/refresh) are stubbed
// under internal/auth/oauth and will be wired here in later milestones.
package auth

import (
	"database/sql"
	"net/http"
	"time"

	"github.com/headercat/airbrew/internal/audit"
	"github.com/headercat/airbrew/internal/auth/handler"
	"github.com/headercat/airbrew/internal/auth/session"
	"github.com/headercat/airbrew/internal/auth/user"
	"github.com/headercat/airbrew/internal/blob"
)

// Config carries auth-module tuning knobs.
type Config struct {
	SessionMaxAge time.Duration
	Blobs         blob.Store
	Audit         *audit.Service
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
	Handler  *handler.Handler
}

// New builds the auth Module bound to the given database.
func New(db *sql.DB, cfg Config) *Module {
	userRepo := user.NewRepository(db)
	userSvc := user.NewService(userRepo)
	sessRepo := session.NewRepository(db)
	sessSvc := session.NewService(sessRepo, cfg.SessionMaxAge)
	h := handler.New(handler.Deps{
		UserRepo:     userRepo,
		UserSvc:      userSvc,
		SessSvc:      sessSvc,
		Blobs:        cfg.Blobs,
		Audit:        cfg.Audit,
		CookieSecure: cfg.CookieSecure,
	})
	return &Module{
		UserRepo: userRepo,
		UserSvc:  userSvc,
		SessRepo: sessRepo,
		SessSvc:  sessSvc,
		Handler:  h,
	}
}

// SessionMiddleware loads any active session into the request context.
// It is best-effort: missing/invalid/expired cookies are silently ignored
// so the rest of the request can decide how to react.
func (m *Module) SessionMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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

// RegisterRoutes mounts all auth-module routes on mux, wrapped in the
// session middleware so /api/auth/me can read the loaded session.
func (m *Module) RegisterRoutes(mux *http.ServeMux) {
	m.Handler.RegisterRoutes(mux)
}
