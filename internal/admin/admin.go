// Package admin is the workspace administration module.
package admin

import (
	"database/sql"
	"net/http"

	"github.com/headercat/airbrew/internal/audit"
	"github.com/headercat/airbrew/internal/auth/oauth"
	"github.com/headercat/airbrew/internal/auth/session"
	"github.com/headercat/airbrew/internal/auth/user"
	"github.com/headercat/airbrew/internal/modules"
	"github.com/headercat/airbrew/internal/security"
)

// Module wires the admin HTTP surface.
type Module struct {
	state    *modules.State
	userRepo *user.Repository
	userSvc  *user.Service
	security *security.Service
	audit    *audit.Service
	handler  *Handler
}

// New builds the admin Module.
func New(db *sql.DB) *Module {
	state := modules.NewState(db)
	userRepo := user.NewRepository(db)
	sec := security.NewService(db)
	userSvc := user.NewService(userRepo)
	userSvc.SetPasswordPolicyChecker(sec)
	oauthRepo := oauth.NewClientRepository(db)
	oauthSvc := oauth.NewClientService(oauthRepo)
	auditSvc := audit.NewService(db)
	h := &Handler{
		db:       db,
		state:    state,
		userRepo: userRepo,
		userSvc:  userSvc,
		security: sec,
		oauthSvc: oauthSvc,
		audit:    auditSvc,
	}
	return &Module{state: state, userRepo: userRepo, userSvc: userSvc, security: sec, audit: auditSvc, handler: h}
}

// Audit returns the audit service so other modules can use it.
func (m *Module) Audit() *audit.Service { return m.audit }

// Security returns the security policy service.
func (m *Module) Security() *security.Service { return m.security }

// RegisterPublicRoutes mounts routes that should be reachable without
// admin authentication (e.g. public branding info for the login page).
func (m *Module) RegisterPublicRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/branding", m.handler.getBrandingPublic)
	mux.HandleFunc("GET /api/admin/status", m.handler.status)
}

// RegisterRoutes mounts the admin-only routes on mux. Every route under
// /api/admin/ (except /status, which is registered publicly above) requires
// an active admin session via RequireAdmin.
func (m *Module) RegisterRoutes(mux *http.ServeMux) {
	admin := http.NewServeMux()
	admin.HandleFunc("GET /api/admin/dashboard", m.handler.dashboard)
	admin.HandleFunc("GET /api/admin/modules", m.handler.listModules)
	admin.HandleFunc("PATCH /api/admin/modules/{key}", m.handler.patchModule)
	admin.HandleFunc("GET /api/admin/users", m.handler.listUsers)
	admin.HandleFunc("POST /api/admin/users", m.handler.createUser)
	admin.HandleFunc("GET /api/admin/users/{id}", m.handler.getUser)
	admin.HandleFunc("PATCH /api/admin/users/{id}", m.handler.patchUser)
	admin.HandleFunc("POST /api/admin/users/{id}/reset-password", m.handler.resetPassword)
	admin.HandleFunc("GET /api/admin/audit", m.handler.listAudit)
	admin.HandleFunc("GET /api/admin/oauth/clients", m.handler.listOAuthClients)
	admin.HandleFunc("POST /api/admin/oauth/clients", m.handler.createOAuthClient)
	admin.HandleFunc("GET /api/admin/oauth/clients/{id}", m.handler.getOAuthClient)
	admin.HandleFunc("PATCH /api/admin/oauth/clients/{id}", m.handler.updateOAuthClient)
	admin.HandleFunc("DELETE /api/admin/oauth/clients/{id}", m.handler.deleteOAuthClient)
	admin.HandleFunc("POST /api/admin/oauth/clients/{id}/secret", m.handler.rotateOAuthClientSecret)
	admin.HandleFunc("GET /api/admin/sessions", m.handler.listSessions)
	admin.HandleFunc("DELETE /api/admin/sessions/{id}", m.handler.revokeSession)
	admin.HandleFunc("GET /api/admin/branding", m.handler.getBranding)
	admin.HandleFunc("PUT /api/admin/branding", m.handler.putBranding)
	admin.HandleFunc("GET /api/admin/system", m.handler.systemInfo)
	admin.HandleFunc("GET /api/admin/security/password-policy", m.handler.getPasswordPolicy)
	admin.HandleFunc("PUT /api/admin/security/password-policy", m.handler.putPasswordPolicy)
	admin.HandleFunc("GET /api/admin/security/ip-allowlist", m.handler.getIPAllowlist)
	admin.HandleFunc("PUT /api/admin/security/ip-allowlist", m.handler.putIPAllowlist)
	admin.HandleFunc("GET /api/admin/security/login-history", m.handler.loginHistory)

	mux.Handle("/api/admin/", RequireAdmin(m.userRepo)(admin))
}

// State returns the module-state service.
func (m *Module) State() *modules.State { return m.state }

// RequireAdmin rejects requests without an active admin session.
func RequireAdmin(repo *user.Repository) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			sess, ok := session.FromContext(r.Context())
			if !ok {
				deny(w, http.StatusUnauthorized, "unauthorized", "no active session")
				return
			}
			u, err := repo.GetByID(r.Context(), sess.UserID)
			if err != nil {
				deny(w, http.StatusUnauthorized, "unauthorized", "user not found")
				return
			}
			if u.Role != user.RoleAdmin || u.Status != user.StatusActive {
				deny(w, http.StatusForbidden, "forbidden", "admin role required")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func deny(w http.ResponseWriter, status int, code, desc string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(`{"error":"` + code + `","error_description":"` + desc + `"}`))
}
