package admin

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/headercat/airbrew/internal/audit"
	"github.com/headercat/airbrew/internal/auth/oauth"
	"github.com/headercat/airbrew/internal/auth/user"
	"github.com/headercat/airbrew/internal/httpserver/response"
	"github.com/headercat/airbrew/internal/modules"
	"github.com/headercat/airbrew/internal/security"
)

// Handler implements the admin HTTP endpoints.
type Handler struct {
	db       *sql.DB
	state    *modules.State
	userRepo *user.Repository
	userSvc  *user.Service
	security *security.Service
	oauthSvc *oauth.ClientService
	audit    *audit.Service
}

type moduleDTO struct {
	Key           string                 `json:"key"`
	Name          string                 `json:"name"`
	Description   string                 `json:"description"`
	AdminOnly     bool                   `json:"admin_only"`
	System        bool                   `json:"system"`
	Enabled       bool                   `json:"enabled"`
	Health        string                 `json:"health"`
	StatusMessage string                 `json:"status_message"`
	SettingsPath  string                 `json:"settings_path"`
	Dependencies  []string               `json:"dependencies"`
	HealthChecks  []moduleHealthCheckDTO `json:"health_checks"`
	DisableImpact string                 `json:"disable_impact"`
}

type moduleHealthCheckDTO struct {
	Key     string `json:"key"`
	Status  string `json:"status"`
	Message string `json:"message"`
}

var startedAt = time.Now().UTC().Truncate(time.Second)

const storedBackupRetention = 10

func (h *Handler) status(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"module":"admin","status":"ok"}`))
}

// ---- Dashboard ----

type dashboardResp struct {
	Users          int              `json:"users"`
	Admins         int              `json:"admins"`
	ActiveSessions int              `json:"active_sessions"`
	ModulesTotal   int              `json:"modules_total"`
	ModulesEnabled int              `json:"modules_enabled"`
	RecentEvents   []audit.LogEntry `json:"recent_events"`
}

func (h *Handler) dashboard(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userCount, _ := h.userRepo.Count(ctx)
	adminCount, _ := h.userRepo.CountActiveByRole(ctx, user.RoleAdmin)
	activeSessions, _ := h.countActiveSessions(ctx)
	states, _ := h.state.AllEnabled(ctx)
	enabled := 0
	for _, on := range states {
		if on {
			enabled++
		}
	}
	recent, _, _ := h.audit.List(ctx, audit.ListFilter{Limit: 10})
	response.JSON(w, http.StatusOK, dashboardResp{
		Users:          userCount,
		Admins:         adminCount,
		ActiveSessions: activeSessions,
		ModulesTotal:   len(modules.Catalog),
		ModulesEnabled: enabled,
		RecentEvents:   recent,
	})
}

func (h *Handler) countActiveSessions(ctx context.Context) (int, error) {
	var n int
	err := h.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sessions
		 WHERE revoked_at IS NULL AND expires_at > ?`,
		time.Now().UTC(),
	).Scan(&n)
	return n, err
}

// ---- Modules ----

func (h *Handler) listModules(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	states, err := h.state.AllEnabled(r.Context())
	if err != nil {
		response.Error(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	dbErr := h.db.PingContext(ctx)
	dbPath, _ := h.databasePath(ctx)
	dataDir := ""
	if dbPath != "" {
		dataDir = filepath.Dir(dbPath)
	}
	out := make([]moduleDTO, 0, len(modules.Catalog))
	for _, m := range modules.Catalog {
		health, message, checks := h.moduleHealthReport(ctx, m, states[m.Key], dbErr, dataDir)
		out = append(out, moduleDTO{
			Key: m.Key, Name: m.Name, Description: m.Description,
			AdminOnly: m.AdminOnly, System: m.System, Enabled: states[m.Key],
			Health:        health,
			StatusMessage: message,
			SettingsPath:  "/admin/modules/" + m.Key,
			Dependencies:  moduleDependencies(m),
			HealthChecks:  checks,
			DisableImpact: moduleDisableImpact(m),
		})
	}
	response.JSON(w, http.StatusOK, map[string]any{"modules": out})
}

func (h *Handler) moduleHealthReport(ctx context.Context, m modules.Meta, enabled bool, dbErr error, dataDir string) (string, string, []moduleHealthCheckDTO) {
	if !m.System && !enabled {
		return "disabled", "Module routes are blocked until an admin enables it.", nil
	}
	checks := []moduleHealthCheckDTO{}
	if dbErr != nil {
		checks = append(checks, moduleHealthCheckDTO{Key: "database", Status: "error", Message: "Database ping failed: " + dbErr.Error()})
	} else {
		checks = append(checks, moduleHealthCheckDTO{Key: "database", Status: "ok", Message: "Database is reachable."})
	}
	if moduleUsesBlobStore(m) {
		total, free, err := diskUsage(dataDir)
		switch {
		case err != nil:
			checks = append(checks, moduleHealthCheckDTO{Key: "storage_volume", Status: "warning", Message: "Data volume could not be checked: " + err.Error()})
		case total > 0 && free < 512*1024*1024:
			checks = append(checks, moduleHealthCheckDTO{Key: "storage_volume", Status: "warning", Message: "Data volume has less than 512 MB free."})
		default:
			checks = append(checks, moduleHealthCheckDTO{Key: "storage_volume", Status: "ok", Message: "Data volume has available capacity."})
		}
	}
	if m.Key == "ai" {
		if h.activeAIProviderConfigured(ctx) {
			checks = append(checks, moduleHealthCheckDTO{Key: "provider_config", Status: "ok", Message: "An active chat provider is configured."})
		} else {
			checks = append(checks, moduleHealthCheckDTO{Key: "provider_config", Status: "warning", Message: "No active chat provider is configured."})
		}
	}
	health := aggregateModuleHealth(checks)
	if m.System && health == "ok" {
		return health, "System module is always available.", checks
	}
	if health == "ok" {
		return health, "Module routes are available to eligible users.", checks
	}
	return health, "Module is enabled, but one or more dependency checks need attention.", checks
}

func aggregateModuleHealth(checks []moduleHealthCheckDTO) string {
	health := "ok"
	for _, c := range checks {
		switch c.Status {
		case "error":
			return "error"
		case "warning":
			health = "warning"
		}
	}
	return health
}

func moduleUsesBlobStore(m modules.Meta) bool {
	switch m.Key {
	case "drive", "contacts", "mail", "passwords":
		return true
	default:
		return false
	}
}

func (h *Handler) activeAIProviderConfigured(ctx context.Context) bool {
	var n int
	err := h.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM ai_provider_configs WHERE direction = 'chat' AND is_active = 1`).Scan(&n)
	return err == nil && n > 0
}

func moduleDependencies(m modules.Meta) []string {
	switch m.Key {
	case "admin":
		return []string{"auth", "audit"}
	case "ai":
		return []string{"auth", "provider_config"}
	case "drive", "contacts", "mail", "passwords":
		return []string{"auth", "blob_store"}
	default:
		return []string{"auth"}
	}
}

func moduleDisableImpact(m modules.Meta) string {
	if m.System {
		return "System modules cannot be disabled."
	}
	return "Disabling this module hides it from navigation and blocks its API routes for non-admin users."
}

type patchModuleReq struct {
	Enabled *bool `json:"enabled,omitempty"`
}

func (h *Handler) patchModule(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	var req patchModuleReq
	if err := decodeJSON(r, &req); err != nil {
		response.Error(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if req.Enabled == nil {
		response.Error(w, http.StatusBadRequest, "invalid_request", "enabled field is required")
		return
	}
	if err := h.state.SetEnabled(r.Context(), key, *req.Enabled); err != nil {
		if errors.Is(err, modules.ErrCannotDisableSystem) {
			response.Error(w, http.StatusBadRequest, "cannot_disable_system", "system modules cannot be disabled")
			return
		}
		response.Error(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	eventType := "module.enabled"
	if !*req.Enabled {
		eventType = "module.disabled"
	}
	h.audit.Log(r.Context(), audit.Entry{
		EventType: eventType, ActorUserID: callerUserID(r),
		TargetType: "module", TargetID: key,
		IPAddress: h.clientIP(r), UserAgent: r.UserAgent(),
	})
	response.JSON(w, http.StatusOK, map[string]any{"key": key, "enabled": *req.Enabled})
}

// ---- Users ----

type userDTO struct {
	ID          string     `json:"id"`
	Email       string     `json:"email"`
	DisplayName string     `json:"display_name"`
	Status      string     `json:"status"`
	Role        string     `json:"role"`
	CreatedAt   time.Time  `json:"created_at"`
	DeletedAt   *time.Time `json:"deleted_at,omitempty"`
}

func toUserDTO(u *user.User) userDTO {
	return userDTO{
		ID: u.ID, Email: u.Email, DisplayName: u.DisplayName,
		Status: string(u.Status), Role: string(u.Role), CreatedAt: u.CreatedAt,
		DeletedAt: u.DeletedAt,
	}
}

func (h *Handler) listUsers(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	search := q.Get("search")
	includeDeleted := q.Get("include_deleted") == "true"
	limit, _ := strconv.Atoi(q.Get("limit"))
	offset, _ := strconv.Atoi(q.Get("offset"))
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	var users []*user.User
	var err error
	if includeDeleted {
		users, err = h.userRepo.SearchIncludingDeleted(r.Context(), search, limit, offset)
	} else {
		users, err = h.userRepo.Search(r.Context(), search, limit, offset)
	}
	if err != nil {
		response.Error(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	total, err := h.userRepo.CountSearch(r.Context(), search, includeDeleted)
	if err != nil {
		response.Error(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	dto := make([]userDTO, 0, len(users))
	for _, u := range users {
		dto = append(dto, toUserDTO(u))
	}
	response.JSON(w, http.StatusOK, map[string]any{
		"users": dto, "total": total, "limit": limit, "offset": offset,
	})
}

type createUserReq struct {
	Email       string `json:"email"`
	Password    string `json:"password"`
	DisplayName string `json:"display_name,omitempty"`
	Role        string `json:"role,omitempty"`
}

func (h *Handler) createUser(w http.ResponseWriter, r *http.Request) {
	var req createUserReq
	if err := decodeJSON(r, &req); err != nil {
		response.Error(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	pw := req.Password
	generated := false
	if pw == "" {
		var err error
		pw, err = generateTempPassword()
		if err != nil {
			response.Error(w, http.StatusInternalServerError, "internal_error", err.Error())
			return
		}
		generated = true
	}
	var u *user.User
	var err error
	if req.Role == "admin" {
		u, err = h.userSvc.RegisterAdmin(r.Context(), req.Email, pw, req.DisplayName)
	} else {
		u, err = h.userSvc.Register(r.Context(), req.Email, pw, req.DisplayName)
	}
	if err != nil {
		if errors.Is(err, user.ErrEmailTaken) {
			response.Error(w, http.StatusConflict, "email_taken", "email already registered")
			return
		}
		response.Error(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	h.audit.Log(r.Context(), audit.Entry{
		EventType: "user.created", ActorUserID: callerUserID(r),
		TargetType: "user", TargetID: u.ID,
		IPAddress: h.clientIP(r), UserAgent: r.UserAgent(),
		Metadata: map[string]any{"email": u.Email, "role": string(u.Role)},
	})
	resp := map[string]any{"user": toUserDTO(u)}
	if generated {
		resp["temp_password"] = pw
	}
	response.JSON(w, http.StatusCreated, resp)
}

func (h *Handler) getUser(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	u, err := h.userRepo.GetByID(r.Context(), id)
	if err != nil {
		response.Error(w, http.StatusNotFound, "not_found", "user not found")
		return
	}
	response.JSON(w, http.StatusOK, toUserDTO(u))
}

type patchUserReq struct {
	Role   *string `json:"role,omitempty"`
	Status *string `json:"status,omitempty"`
}

func (h *Handler) patchUser(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req patchUserReq
	if err := decodeJSON(r, &req); err != nil {
		response.Error(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	target, err := h.userRepo.GetByID(r.Context(), id)
	if err != nil {
		response.Error(w, http.StatusNotFound, "not_found", "user not found")
		return
	}
	callerID := callerUserID(r)

	if req.Role != nil {
		newRole := user.Role(*req.Role)
		if newRole != user.RoleUser && newRole != user.RoleAdmin {
			response.Error(w, http.StatusBadRequest, "invalid_request", "role must be 'user' or 'admin'")
			return
		}
		if id == callerID && newRole != user.RoleAdmin {
			response.Error(w, http.StatusBadRequest, "cannot_demote_self", "you cannot remove your own admin role")
			return
		}
		if target.Role == user.RoleAdmin && newRole == user.RoleUser {
			count, err := h.userRepo.CountActiveByRole(r.Context(), user.RoleAdmin)
			if err != nil {
				response.Error(w, http.StatusInternalServerError, "internal_error", err.Error())
				return
			}
			if count <= 1 {
				response.Error(w, http.StatusBadRequest, "last_admin", "cannot demote the last admin")
				return
			}
		}
		if err := h.userRepo.SetRole(r.Context(), id, newRole); err != nil {
			response.Error(w, http.StatusInternalServerError, "internal_error", err.Error())
			return
		}
		h.audit.Log(r.Context(), audit.Entry{
			EventType: "user.role_changed", ActorUserID: callerID,
			TargetType: "user", TargetID: id,
			IPAddress: h.clientIP(r), UserAgent: r.UserAgent(),
			Metadata: map[string]any{"from": string(target.Role), "to": string(newRole)},
		})
	}

	if req.Status != nil {
		newStatus := user.Status(*req.Status)
		if newStatus != user.StatusActive && newStatus != user.StatusSuspended && newStatus != user.StatusDeleted {
			response.Error(w, http.StatusBadRequest, "invalid_request", "status must be active, suspended or deleted")
			return
		}
		if id == callerID && newStatus != user.StatusActive {
			response.Error(w, http.StatusBadRequest, "cannot_suspend_self", "you cannot suspend or delete yourself")
			return
		}
		if target.Role == user.RoleAdmin && newStatus != user.StatusActive {
			count, err := h.userRepo.CountActiveByRole(r.Context(), user.RoleAdmin)
			if err != nil {
				response.Error(w, http.StatusInternalServerError, "internal_error", err.Error())
				return
			}
			if count <= 1 {
				response.Error(w, http.StatusBadRequest, "last_admin", "cannot suspend the last admin")
				return
			}
		}
		if err := h.userRepo.SetStatus(r.Context(), id, newStatus); err != nil {
			response.Error(w, http.StatusInternalServerError, "internal_error", err.Error())
			return
		}
		revoked := int64(0)
		if newStatus != user.StatusActive {
			revoked, err = h.revokeActiveSessionsForUser(r.Context(), id)
			if err != nil {
				response.Error(w, http.StatusInternalServerError, "internal_error", err.Error())
				return
			}
		}
		h.audit.Log(r.Context(), audit.Entry{
			EventType: "user.status_changed", ActorUserID: callerID,
			TargetType: "user", TargetID: id,
			IPAddress: h.clientIP(r), UserAgent: r.UserAgent(),
			Metadata: map[string]any{
				"from": string(target.Status), "to": string(newStatus),
				"revoked_sessions": revoked,
			},
		})
	}

	updated, err := h.userRepo.GetByID(r.Context(), id)
	if err != nil {
		response.Error(w, http.StatusNotFound, "not_found", "user not found")
		return
	}
	response.JSON(w, http.StatusOK, toUserDTO(updated))
}

func (h *Handler) deleteUser(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	target, err := h.userRepo.GetByID(r.Context(), id)
	if err != nil {
		response.Error(w, http.StatusNotFound, "not_found", "user not found")
		return
	}
	callerID := callerUserID(r)
	if id == callerID {
		response.Error(w, http.StatusBadRequest, "cannot_delete_self", "you cannot delete yourself")
		return
	}
	if target.Role == user.RoleAdmin {
		count, err := h.userRepo.CountActiveByRole(r.Context(), user.RoleAdmin)
		if err != nil {
			response.Error(w, http.StatusInternalServerError, "internal_error", err.Error())
			return
		}
		if count <= 1 {
			response.Error(w, http.StatusBadRequest, "last_admin", "cannot delete the last admin")
			return
		}
	}
	if err := h.userRepo.SetStatus(r.Context(), id, user.StatusDeleted); err != nil {
		response.Error(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	revoked, err := h.revokeActiveSessionsForUser(r.Context(), id)
	if err != nil {
		response.Error(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	h.audit.Log(r.Context(), audit.Entry{
		EventType: "user.deleted", ActorUserID: callerID,
		TargetType: "user", TargetID: id,
		IPAddress: h.clientIP(r), UserAgent: r.UserAgent(),
		Metadata: map[string]any{
			"from": string(target.Status), "to": string(user.StatusDeleted),
			"revoked_sessions": revoked,
		},
	})
	response.JSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (h *Handler) restoreUser(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	target, err := h.userRepo.GetByID(r.Context(), id)
	if err != nil {
		response.Error(w, http.StatusNotFound, "not_found", "user not found")
		return
	}
	if target.Status != user.StatusDeleted {
		response.Error(w, http.StatusBadRequest, "invalid_request", "user is not deleted")
		return
	}
	if target.DeletedAt != nil && time.Since(*target.DeletedAt) > 30*24*time.Hour {
		response.Error(w, http.StatusBadRequest, "recovery_window_expired", "user recovery window has expired")
		return
	}
	if err := h.userRepo.SetStatus(r.Context(), id, user.StatusActive); err != nil {
		response.Error(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	h.audit.Log(r.Context(), audit.Entry{
		EventType: "user.restored", ActorUserID: callerUserID(r),
		TargetType: "user", TargetID: id,
		IPAddress: h.clientIP(r), UserAgent: r.UserAgent(),
		Metadata: map[string]any{"from": string(user.StatusDeleted), "to": string(user.StatusActive)},
	})
	updated, _ := h.userRepo.GetByID(r.Context(), id)
	response.JSON(w, http.StatusOK, toUserDTO(updated))
}

type resetPasswordReq struct {
	NewPassword string `json:"new_password,omitempty"`
}

func (h *Handler) resetPassword(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req resetPasswordReq
	if err := decodeJSON(r, &req); err != nil {
		response.Error(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	pw := req.NewPassword
	generated := false
	if pw == "" {
		var err error
		pw, err = generateTempPassword()
		if err != nil {
			response.Error(w, http.StatusInternalServerError, "internal_error", err.Error())
			return
		}
		generated = true
	}
	if err := h.userSvc.AdminSetPassword(r.Context(), id, pw); err != nil {
		response.Error(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	revoked, err := h.revokeActiveSessionsForUser(r.Context(), id)
	if err != nil {
		response.Error(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	h.audit.Log(r.Context(), audit.Entry{
		EventType: "user.password_reset", ActorUserID: callerUserID(r),
		TargetType: "user", TargetID: id,
		IPAddress: h.clientIP(r), UserAgent: r.UserAgent(),
		Metadata: map[string]any{"revoked_sessions": revoked},
	})
	resp := map[string]any{"ok": true, "revoked_sessions": revoked}
	if generated {
		resp["temp_password"] = pw
	}
	response.JSON(w, http.StatusOK, resp)
}

// ---- Audit Log ----

func (h *Handler) listAudit(w http.ResponseWriter, r *http.Request) {
	f := auditFilterFromRequest(r)
	entries, total, err := h.audit.List(r.Context(), f)
	if err != nil {
		response.Error(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	response.JSON(w, http.StatusOK, map[string]any{
		"entries": entries, "total": total,
		"limit": f.Limit, "offset": f.Offset,
	})
}

func (h *Handler) exportAudit(w http.ResponseWriter, r *http.Request) {
	f := auditFilterFromRequest(r)
	if f.To.IsZero() {
		f.To = time.Now().UTC()
	}
	total, err := h.writeAuditCSV(w, r, f)
	if err != nil {
		return
	}
	h.audit.Log(context.Background(), audit.Entry{
		EventType: "audit.exported", ActorUserID: callerUserID(r),
		TargetType: "audit_log", TargetID: "csv",
		IPAddress: h.clientIP(r), UserAgent: r.UserAgent(),
		Metadata: map[string]any{"rows": total, "to": f.To.Format(time.RFC3339)},
	})
}

func auditFilterFromRequest(r *http.Request) audit.ListFilter {
	q := r.URL.Query()
	f := audit.ListFilter{
		EventType:  q.Get("event_type"),
		ActorID:    q.Get("actor"),
		TargetType: q.Get("target_type"),
		TargetID:   q.Get("target_id"),
	}
	if v := q.Get("from"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			f.From = t
		}
	}
	if v := q.Get("to"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			f.To = t
		}
	}
	f.Limit, _ = strconv.Atoi(q.Get("limit"))
	f.Offset, _ = strconv.Atoi(q.Get("offset"))
	return f
}

func (h *Handler) writeAuditCSV(w http.ResponseWriter, r *http.Request, f audit.ListFilter) (int, error) {
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="airbrew-audit.csv"`)
	cw := csv.NewWriter(w)
	if err := cw.Write([]string{
		"created_at", "event_type", "actor_user_id", "actor_client_id", "actor_email",
		"target_type", "target_id", "ip_address", "user_agent", "metadata",
	}); err != nil {
		return 0, err
	}
	exported := 0
	for offset := 0; ; {
		f.Limit = 200
		f.Offset = offset
		entries, _, err := h.audit.List(r.Context(), f)
		if err != nil {
			return exported, err
		}
		for _, e := range entries {
			if err := cw.Write([]string{
				e.CreatedAt.UTC().Format(time.RFC3339),
				e.EventType,
				e.ActorUserID,
				e.ActorClientID,
				e.ActorEmail,
				e.TargetType,
				e.TargetID,
				e.IPAddress,
				e.UserAgent,
				e.Metadata,
			}); err != nil {
				return exported, err
			}
			exported++
		}
		if len(entries) == 0 {
			break
		}
		offset += len(entries)
	}
	cw.Flush()
	return exported, cw.Error()
}

// ---- OAuth Clients ----

type oauthClientDTO struct {
	ID                      string    `json:"id"`
	ClientID                string    `json:"client_id"`
	Name                    string    `json:"name"`
	ClientType              string    `json:"client_type"`
	TokenEndpointAuthMethod string    `json:"token_endpoint_auth_method"`
	AllowedScopes           []string  `json:"allowed_scopes"`
	RedirectURIs            []string  `json:"redirect_uris"`
	PostLogoutRedirectURIs  []string  `json:"post_logout_redirect_uris"`
	IsFirstParty            bool      `json:"is_first_party"`
	RequireConsent          bool      `json:"require_consent"`
	IsActive                bool      `json:"is_active"`
	CreatedAt               time.Time `json:"created_at"`
	UpdatedAt               time.Time `json:"updated_at"`
}

type createOAuthClientReq struct {
	Name                    string   `json:"name"`
	ClientType              string   `json:"client_type"`
	TokenEndpointAuthMethod string   `json:"token_endpoint_auth_method,omitempty"`
	AllowedScopes           []string `json:"allowed_scopes,omitempty"`
	RedirectURIs            []string `json:"redirect_uris"`
	PostLogoutRedirectURIs  []string `json:"post_logout_redirect_uris,omitempty"`
	IsFirstParty            bool     `json:"is_first_party,omitempty"`
	RequireConsent          bool     `json:"require_consent,omitempty"`
}

type updateOAuthClientReq struct {
	Name                    string   `json:"name"`
	TokenEndpointAuthMethod string   `json:"token_endpoint_auth_method,omitempty"`
	AllowedScopes           []string `json:"allowed_scopes,omitempty"`
	RedirectURIs            []string `json:"redirect_uris"`
	PostLogoutRedirectURIs  []string `json:"post_logout_redirect_uris,omitempty"`
	IsFirstParty            bool     `json:"is_first_party,omitempty"`
	RequireConsent          bool     `json:"require_consent,omitempty"`
	IsActive                bool     `json:"is_active"`
}

func toOAuthClientDTO(c *oauth.Client) oauthClientDTO {
	return oauthClientDTO{
		ID:                      c.ID,
		ClientID:                c.ClientID,
		Name:                    c.Name,
		ClientType:              string(c.ClientType),
		TokenEndpointAuthMethod: string(c.TokenEndpointAuthMethod),
		AllowedScopes:           c.AllowedScopes,
		RedirectURIs:            c.RedirectURIs,
		PostLogoutRedirectURIs:  c.PostLogoutRedirectURIs,
		IsFirstParty:            c.IsFirstParty,
		RequireConsent:          c.RequireConsent,
		IsActive:                c.IsActive,
		CreatedAt:               c.CreatedAt,
		UpdatedAt:               c.UpdatedAt,
	}
}

func (h *Handler) listOAuthClients(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	offset, _ := strconv.Atoi(q.Get("offset"))
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	clients, total, err := h.oauthSvc.List(r.Context(), limit, offset)
	if err != nil {
		response.Error(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	out := make([]oauthClientDTO, 0, len(clients))
	for _, c := range clients {
		out = append(out, toOAuthClientDTO(c))
	}
	response.JSON(w, http.StatusOK, map[string]any{
		"clients": out, "total": total, "limit": limit, "offset": offset,
	})
}

func (h *Handler) createOAuthClient(w http.ResponseWriter, r *http.Request) {
	var req createOAuthClientReq
	if err := decodeJSON(r, &req); err != nil {
		response.Error(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	res, err := h.oauthSvc.Create(r.Context(), oauth.ClientCreate{
		Name:                    req.Name,
		ClientType:              oauth.ClientType(req.ClientType),
		TokenEndpointAuthMethod: oauth.TokenEndpointAuthMethod(req.TokenEndpointAuthMethod),
		AllowedScopes:           req.AllowedScopes,
		RedirectURIs:            req.RedirectURIs,
		PostLogoutRedirectURIs:  req.PostLogoutRedirectURIs,
		IsFirstParty:            req.IsFirstParty,
		RequireConsent:          req.RequireConsent,
	})
	if err != nil {
		code, status := oauthClientError(err)
		response.Error(w, status, code, err.Error())
		return
	}
	h.audit.Log(r.Context(), audit.Entry{
		EventType: "oauth.client_created", ActorUserID: callerUserID(r),
		TargetType: "oauth_client", TargetID: res.Client.ID,
		IPAddress: h.clientIP(r), UserAgent: r.UserAgent(),
		Metadata: map[string]any{
			"client_id":   res.Client.ClientID,
			"client_type": string(res.Client.ClientType),
			"name":        res.Client.Name,
		},
	})
	resp := map[string]any{"client": toOAuthClientDTO(res.Client)}
	if res.ClientSecret != "" {
		resp["client_secret"] = res.ClientSecret
	}
	response.JSON(w, http.StatusCreated, resp)
}

func (h *Handler) getOAuthClient(w http.ResponseWriter, r *http.Request) {
	c, err := h.oauthSvc.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		if errors.Is(err, oauth.ErrClientNotFound) {
			response.Error(w, http.StatusNotFound, "not_found", "OAuth client not found")
			return
		}
		response.Error(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	response.JSON(w, http.StatusOK, toOAuthClientDTO(c))
}

func (h *Handler) updateOAuthClient(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req updateOAuthClientReq
	if err := decodeJSON(r, &req); err != nil {
		response.Error(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	c, err := h.oauthSvc.Update(r.Context(), id, oauth.ClientUpdate{
		Name:                    req.Name,
		TokenEndpointAuthMethod: oauth.TokenEndpointAuthMethod(req.TokenEndpointAuthMethod),
		AllowedScopes:           req.AllowedScopes,
		RedirectURIs:            req.RedirectURIs,
		PostLogoutRedirectURIs:  req.PostLogoutRedirectURIs,
		IsFirstParty:            req.IsFirstParty,
		RequireConsent:          req.RequireConsent,
		IsActive:                req.IsActive,
	})
	if err != nil {
		if errors.Is(err, oauth.ErrClientNotFound) {
			response.Error(w, http.StatusNotFound, "not_found", "OAuth client not found")
			return
		}
		code, status := oauthClientError(err)
		response.Error(w, status, code, err.Error())
		return
	}
	revoked := oauth.IssuedCredentialRevocation{}
	if !c.IsActive {
		revoked, err = h.oauthSvc.RevokeIssuedCredentials(r.Context(), id)
		if err != nil {
			response.Error(w, http.StatusInternalServerError, "internal_error", err.Error())
			return
		}
	}
	h.audit.Log(r.Context(), audit.Entry{
		EventType: "oauth.client_updated", ActorUserID: callerUserID(r),
		TargetType: "oauth_client", TargetID: c.ID,
		IPAddress: h.clientIP(r), UserAgent: r.UserAgent(),
		Metadata: map[string]any{
			"client_id": c.ClientID, "name": c.Name, "is_active": c.IsActive,
			"revoked_authorization_codes": revoked.AuthorizationCodes,
			"revoked_refresh_tokens":      revoked.RefreshTokens,
		},
	})
	response.JSON(w, http.StatusOK, toOAuthClientDTO(c))
}

func (h *Handler) deleteOAuthClient(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	c, err := h.oauthSvc.Get(r.Context(), id)
	if err != nil {
		if errors.Is(err, oauth.ErrClientNotFound) {
			response.Error(w, http.StatusNotFound, "not_found", "OAuth client not found")
			return
		}
		response.Error(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	if err := h.oauthSvc.Delete(r.Context(), id); err != nil {
		if errors.Is(err, oauth.ErrClientNotFound) {
			response.Error(w, http.StatusNotFound, "not_found", "OAuth client not found")
			return
		}
		response.Error(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	revoked, err := h.oauthSvc.RevokeIssuedCredentials(r.Context(), id)
	if err != nil {
		response.Error(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	h.audit.Log(r.Context(), audit.Entry{
		EventType: "oauth.client_deleted", ActorUserID: callerUserID(r),
		TargetType: "oauth_client", TargetID: id,
		IPAddress: h.clientIP(r), UserAgent: r.UserAgent(),
		Metadata: map[string]any{
			"client_id": c.ClientID, "name": c.Name,
			"revoked_authorization_codes": revoked.AuthorizationCodes,
			"revoked_refresh_tokens":      revoked.RefreshTokens,
		},
	})
	response.JSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// rotateOAuthClientSecret issues a fresh one-time secret for a confidential
// client. The previous secret stops working immediately.
func (h *Handler) rotateOAuthClientSecret(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	secret, err := h.oauthSvc.RotateSecret(r.Context(), id)
	if err != nil {
		if errors.Is(err, oauth.ErrClientNotFound) {
			response.Error(w, http.StatusNotFound, "not_found", "OAuth client not found")
			return
		}
		if errors.Is(err, oauth.ErrPublicClientSecret) {
			response.Error(w, http.StatusBadRequest, "invalid_request", "only confidential clients have a secret")
			return
		}
		response.Error(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	revoked, err := h.oauthSvc.RevokeIssuedCredentials(r.Context(), id)
	if err != nil {
		response.Error(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	h.audit.Log(r.Context(), audit.Entry{
		EventType: "oauth.client_secret_rotated", ActorUserID: callerUserID(r),
		TargetType: "oauth_client", TargetID: id,
		IPAddress: h.clientIP(r), UserAgent: r.UserAgent(),
		Metadata: map[string]any{
			"revoked_authorization_codes": revoked.AuthorizationCodes,
			"revoked_refresh_tokens":      revoked.RefreshTokens,
		},
	})
	response.JSON(w, http.StatusOK, map[string]string{"client_secret": secret})
}

// oauthClientError maps an oauth.ClientService validation/persistence error to
// an OAuth-style error code and HTTP status.
func oauthClientError(err error) (string, int) {
	switch {
	case errors.Is(err, oauth.ErrClientIDTaken):
		return "client_id_taken", http.StatusConflict
	case errors.Is(err, oauth.ErrNameRequired),
		errors.Is(err, oauth.ErrNameTooLong),
		errors.Is(err, oauth.ErrInvalidClientType),
		errors.Is(err, oauth.ErrPublicClientAuthMethod),
		errors.Is(err, oauth.ErrConfidentialAuthMethod),
		errors.Is(err, oauth.ErrRedirectURIRequired),
		errors.Is(err, oauth.ErrInvalidRedirectURI),
		errors.Is(err, oauth.ErrTooManyRedirectURIs),
		errors.Is(err, oauth.ErrTooManyPostLogoutURIs),
		errors.Is(err, oauth.ErrTooManyScopes):
		return "invalid_request", http.StatusBadRequest
	default:
		return "invalid_request", http.StatusBadRequest
	}
}

// ---- helpers ----

func decodeJSON(r *http.Request, v any) error {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil {
		return host
	}
	return strings.TrimSpace(r.RemoteAddr)
}

func (h *Handler) clientIP(r *http.Request) string {
	if h.security == nil {
		return clientIP(r)
	}
	ip, err := h.security.RequestIP(r.Context(), r)
	if err != nil {
		return clientIP(r)
	}
	return ip
}

// validateCIDR returns an error when raw is not a parseable IPv4/IPv6 CIDR.
// A bare IP (no mask) is accepted and treated as a /32 (v4) or /128 (v6).
func validateCIDR(raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return errors.New("empty CIDR")
	}
	if !strings.Contains(raw, "/") {
		ip := net.ParseIP(raw)
		if ip == nil {
			return fmt.Errorf("invalid IP or CIDR: %s", raw)
		}
		return nil
	}
	if _, _, err := net.ParseCIDR(raw); err != nil {
		return fmt.Errorf("invalid CIDR %q: %w", raw, err)
	}
	return nil
}

func generateTempPassword() (string, error) {
	b := make([]byte, 18)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// ---- Sessions ----

type sessionDTO struct {
	ID        string    `json:"id"`
	UserID    string    `json:"user_id"`
	UserEmail string    `json:"user_email"`
	IPAddress string    `json:"ip_address"`
	UserAgent string    `json:"user_agent"`
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at"`
}

func (h *Handler) listSessions(w http.ResponseWriter, r *http.Request) {
	rows, err := h.db.QueryContext(r.Context(), `
		SELECT s.id, s.user_id, COALESCE(u.email,''), s.ip_address, s.user_agent, s.created_at, s.expires_at
		FROM sessions s
		LEFT JOIN users u ON u.id = s.user_id
		WHERE s.revoked_at IS NULL AND s.expires_at > ?
		ORDER BY s.created_at DESC
		LIMIT 200
	`, time.Now().UTC())
	if err != nil {
		response.Error(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	defer rows.Close()
	var out []sessionDTO
	for rows.Next() {
		var s sessionDTO
		var ip, ua sql.NullString
		if err := rows.Scan(&s.ID, &s.UserID, &s.UserEmail, &ip, &ua, &s.CreatedAt, &s.ExpiresAt); err != nil {
			response.Error(w, http.StatusInternalServerError, "internal_error", err.Error())
			return
		}
		s.IPAddress = ip.String
		s.UserAgent = ua.String
		out = append(out, s)
	}
	response.JSON(w, http.StatusOK, map[string]any{"sessions": out})
}

func (h *Handler) revokeSession(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	now := time.Now().UTC().Truncate(time.Second)
	res, err := h.db.ExecContext(r.Context(),
		"UPDATE sessions SET revoked_at = ? WHERE id = ? AND revoked_at IS NULL", now, id)
	if err != nil {
		response.Error(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		response.Error(w, http.StatusNotFound, "not_found", "session not found or already revoked")
		return
	}
	h.audit.Log(r.Context(), audit.Entry{
		EventType: "session.revoked", ActorUserID: callerUserID(r),
		TargetType: "session", TargetID: id,
		IPAddress: h.clientIP(r), UserAgent: r.UserAgent(),
	})
	response.JSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (h *Handler) listUserSessions(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, err := h.userRepo.GetByID(r.Context(), id); err != nil {
		response.Error(w, http.StatusNotFound, "not_found", "user not found")
		return
	}
	rows, err := h.db.QueryContext(r.Context(), `
		SELECT s.id, s.user_id, COALESCE(u.email,''), s.ip_address, s.user_agent, s.created_at, s.expires_at
		FROM sessions s
		LEFT JOIN users u ON u.id = s.user_id
		WHERE s.user_id = ? AND s.revoked_at IS NULL AND s.expires_at > ?
		ORDER BY s.created_at DESC
		LIMIT 100
	`, id, time.Now().UTC())
	if err != nil {
		response.Error(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	defer rows.Close()
	var out []sessionDTO
	for rows.Next() {
		var s sessionDTO
		var ip, ua sql.NullString
		if err := rows.Scan(&s.ID, &s.UserID, &s.UserEmail, &ip, &ua, &s.CreatedAt, &s.ExpiresAt); err != nil {
			response.Error(w, http.StatusInternalServerError, "internal_error", err.Error())
			return
		}
		s.IPAddress = ip.String
		s.UserAgent = ua.String
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		response.Error(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	response.JSON(w, http.StatusOK, map[string]any{"sessions": out})
}

func (h *Handler) revokeUserSessions(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, err := h.userRepo.GetByID(r.Context(), id); err != nil {
		response.Error(w, http.StatusNotFound, "not_found", "user not found")
		return
	}
	n, err := h.revokeActiveSessionsForUser(r.Context(), id)
	if err != nil {
		response.Error(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	h.audit.Log(r.Context(), audit.Entry{
		EventType: "session.revoked", ActorUserID: callerUserID(r),
		TargetType: "user", TargetID: id,
		IPAddress: h.clientIP(r), UserAgent: r.UserAgent(),
		Metadata: map[string]any{"count": n},
	})
	response.JSON(w, http.StatusOK, map[string]any{"ok": true, "revoked": n})
}

func (h *Handler) revokeActiveSessionsForUser(ctx context.Context, userID string) (int64, error) {
	now := time.Now().UTC().Truncate(time.Second)
	res, err := h.db.ExecContext(ctx, `
		UPDATE sessions
		SET revoked_at = ?
		WHERE user_id = ? AND revoked_at IS NULL AND expires_at > ?
	`, now, userID, now)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}

func (h *Handler) userActivity(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, err := h.userRepo.GetByID(r.Context(), id); err != nil {
		response.Error(w, http.StatusNotFound, "not_found", "user not found")
		return
	}
	entries, total, err := h.audit.List(r.Context(), audit.ListFilter{
		TargetType: "user",
		TargetID:   id,
		Limit:      25,
	})
	if err != nil {
		response.Error(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	response.JSON(w, http.StatusOK, map[string]any{"entries": entries, "total": total})
}

// ---- Branding ----

type brandingDTO struct {
	WorkspaceName string `json:"workspace_name"`
	LogoURL       string `json:"logo_url"`
	PrimaryColor  string `json:"primary_color"`
}

func defaultBranding() brandingDTO {
	return brandingDTO{
		WorkspaceName: "Airbrew",
		PrimaryColor:  "215 55% 48%",
	}
}

func (h *Handler) getBrandingPublic(w http.ResponseWriter, r *http.Request) {
	b := h.loadBranding(r.Context())
	response.JSON(w, http.StatusOK, b)
}

func (h *Handler) getBranding(w http.ResponseWriter, r *http.Request) {
	b := h.loadBranding(r.Context())
	response.JSON(w, http.StatusOK, b)
}

type putBrandingReq struct {
	WorkspaceName *string `json:"workspace_name,omitempty"`
	LogoURL       *string `json:"logo_url,omitempty"`
	PrimaryColor  *string `json:"primary_color,omitempty"`
}

func (h *Handler) putBranding(w http.ResponseWriter, r *http.Request) {
	var req putBrandingReq
	if err := decodeJSON(r, &req); err != nil {
		response.Error(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if req.WorkspaceName != nil {
		v := strings.TrimSpace(*req.WorkspaceName)
		if len(v) > 100 {
			response.Error(w, http.StatusBadRequest, "invalid_request", "workspace name max 100 chars")
			return
		}
		h.setSetting(r.Context(), "branding.workspace_name", v)
	}
	if req.LogoURL != nil {
		v, err := normalizeLogoURL(*req.LogoURL)
		if err != nil {
			response.Error(w, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
		h.setSetting(r.Context(), "branding.logo_url", v)
	}
	if req.PrimaryColor != nil {
		v := strings.TrimSpace(*req.PrimaryColor)
		h.setSetting(r.Context(), "branding.primary_color", v)
	}
	h.audit.Log(r.Context(), audit.Entry{
		EventType: "branding.updated", ActorUserID: callerUserID(r),
		IPAddress: h.clientIP(r), UserAgent: r.UserAgent(),
	})
	b := h.loadBranding(r.Context())
	response.JSON(w, http.StatusOK, b)
}

func (h *Handler) loadBranding(ctx context.Context) brandingDTO {
	b := defaultBranding()
	rows, err := h.db.QueryContext(ctx,
		"SELECT key, value FROM server_settings WHERE key LIKE 'branding.%'")
	if err != nil {
		return b
	}
	defer rows.Close()
	for rows.Next() {
		var k, v string
		_ = rows.Scan(&k, &v)
		switch k {
		case "branding.workspace_name":
			if v != "" {
				b.WorkspaceName = v
			}
		case "branding.logo_url":
			b.LogoURL = v
		case "branding.primary_color":
			if v != "" {
				b.PrimaryColor = v
			}
		}
	}
	return b
}

func normalizeLogoURL(raw string) (string, error) {
	v := strings.TrimSpace(raw)
	if v == "" {
		return "", nil
	}
	if strings.HasPrefix(v, "/") && !strings.HasPrefix(v, "//") {
		return v, nil
	}
	u, err := url.Parse(v)
	if err != nil || u.Host == "" {
		return "", errors.New("logo URL must be https, http, or root-relative")
	}
	if u.Scheme != "https" && u.Scheme != "http" {
		return "", errors.New("logo URL must be https, http, or root-relative")
	}
	return v, nil
}

func (h *Handler) setSetting(ctx context.Context, key, value string) {
	now := time.Now().UTC().Truncate(time.Second)
	_, _ = h.db.ExecContext(ctx, `
		INSERT INTO server_settings (key, value, updated_at) VALUES (?, ?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at
	`, key, value, now)
}

// ---- System ----

type systemDTO struct {
	Version          string    `json:"version"`
	GoVersion        string    `json:"go_version"`
	StartedAt        time.Time `json:"started_at"`
	UptimeSeconds    int64     `json:"uptime_seconds"`
	NumCPU           int       `json:"num_cpu"`
	UserCount        int       `json:"user_count"`
	ModuleEnabled    int       `json:"modules_enabled"`
	ModuleTotal      int       `json:"modules_total"`
	SessionCount     int       `json:"active_sessions"`
	DBPath           string    `json:"db_path"`
	DataDir          string    `json:"data_dir"`
	DBSizeMB         string    `json:"db_size_mb"`
	DiskFreeBytes    int64     `json:"disk_free_bytes"`
	DiskTotalBytes   int64     `json:"disk_total_bytes"`
	MigrationVersion string    `json:"migration_version"`
}

func (h *Handler) systemInfo(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userCount, _ := h.userRepo.Count(ctx)
	states, _ := h.state.AllEnabled(ctx)
	enabled := 0
	for _, on := range states {
		if on {
			enabled++
		}
	}
	activeSessions, _ := h.countActiveSessions(ctx)

	// Get DB file size from the database itself.
	dbSize := "unknown"
	dbPath, _ := h.databasePath(ctx)
	dataDir := ""
	if dbPath != "" {
		dataDir = filepath.Dir(dbPath)
		if fi, err := os.Stat(dbPath); err == nil {
			dbSize = fmt.Sprintf("%.1f", float64(fi.Size())/1024/1024)
		}
	}
	migrationVersion, _ := h.latestMigrationVersion(ctx)
	diskTotal, diskFree, _ := diskUsage(dataDir)

	response.JSON(w, http.StatusOK, systemDTO{
		Version:          "0.1.0",
		GoVersion:        runtime.Version(),
		StartedAt:        startedAt,
		UptimeSeconds:    int64(time.Since(startedAt).Seconds()),
		NumCPU:           runtime.NumCPU(),
		UserCount:        userCount,
		ModuleEnabled:    enabled,
		ModuleTotal:      len(modules.Catalog),
		SessionCount:     activeSessions,
		DBPath:           filepath.Base(dbPath),
		DataDir:          dataDir,
		DBSizeMB:         dbSize,
		DiskFreeBytes:    diskFree,
		DiskTotalBytes:   diskTotal,
		MigrationVersion: migrationVersion,
	})
}

func diskUsage(path string) (int64, int64, error) {
	if path == "" {
		return 0, 0, errors.New("path is empty")
	}
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, 0, err
	}
	blockSize := uint64(st.Bsize)
	return int64(st.Blocks * blockSize), int64(st.Bavail * blockSize), nil
}

func (h *Handler) backupDatabase(w http.ResponseWriter, r *http.Request) {
	dbPath, err := h.databasePath(r.Context())
	if err != nil || dbPath == "" {
		response.Error(w, http.StatusInternalServerError, "backup_unavailable", "database file path is unavailable")
		return
	}
	tmp, err := os.CreateTemp("", "airbrew-backup-*.sqlite")
	if err != nil {
		response.Error(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	tmpPath := tmp.Name()
	_ = tmp.Close()
	defer os.Remove(tmpPath)

	escaped := strings.ReplaceAll(tmpPath, "'", "''")
	if _, err := h.db.ExecContext(r.Context(), "VACUUM INTO '"+escaped+"'"); err != nil {
		response.Error(w, http.StatusInternalServerError, "backup_failed", err.Error())
		return
	}

	h.audit.Log(r.Context(), audit.Entry{
		EventType: "system.backup_downloaded", ActorUserID: callerUserID(r),
		TargetType: "system", TargetID: filepath.Base(dbPath),
		IPAddress: h.clientIP(r), UserAgent: r.UserAgent(),
	})

	filename := "airbrew-backup-" + time.Now().UTC().Format("20060102T150405Z") + ".sqlite"
	w.Header().Set("Content-Type", "application/vnd.sqlite3")
	w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
	http.ServeFile(w, r, tmpPath)
}

type backupVerifyDTO struct {
	OK             bool   `json:"ok"`
	IntegrityCheck string `json:"integrity_check"`
	QuickCheck     string `json:"quick_check"`
	SchemaCheck    string `json:"schema_check"`
	SizeBytes      int64  `json:"size_bytes"`
	SHA256         string `json:"sha256"`
	GeneratedAt    string `json:"generated_at"`
	Migration      string `json:"migration_version"`
}

type storedBackupDTO struct {
	Name      string    `json:"name"`
	SizeBytes int64     `json:"size_bytes"`
	CreatedAt time.Time `json:"created_at"`
}

type restoreStageDTO struct {
	OK             bool            `json:"ok"`
	PendingPath    string          `json:"pending_path"`
	ManifestPath   string          `json:"manifest_path"`
	StagedAt       string          `json:"staged_at"`
	Verification   backupVerifyDTO `json:"verification"`
	RestartMessage string          `json:"restart_message"`
}

func (h *Handler) verifyDatabaseBackup(w http.ResponseWriter, r *http.Request) {
	dbPath, err := h.databasePath(r.Context())
	if err != nil || dbPath == "" {
		response.Error(w, http.StatusInternalServerError, "backup_unavailable", "database file path is unavailable")
		return
	}
	tmp, err := os.CreateTemp("", "airbrew-backup-verify-*.sqlite")
	if err != nil {
		response.Error(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	tmpPath := tmp.Name()
	_ = tmp.Close()
	defer os.Remove(tmpPath)

	escaped := strings.ReplaceAll(tmpPath, "'", "''")
	if _, err := h.db.ExecContext(r.Context(), "VACUUM INTO '"+escaped+"'"); err != nil {
		response.Error(w, http.StatusInternalServerError, "backup_failed", err.Error())
		return
	}
	hash, size, err := fileSHA256(tmpPath)
	if err != nil {
		response.Error(w, http.StatusInternalServerError, "backup_failed", err.Error())
		return
	}
	backupDB, err := sql.Open("sqlite", "file:"+tmpPath+"?mode=ro&_time_format=sqlite")
	if err != nil {
		response.Error(w, http.StatusInternalServerError, "backup_failed", err.Error())
		return
	}
	defer backupDB.Close()
	integrity := sqlitePragmaString(backupDB, r.Context(), "PRAGMA integrity_check")
	quick := sqlitePragmaString(backupDB, r.Context(), "PRAGMA quick_check")
	schema := airbrewSchemaCheck(backupDB, r.Context())
	migration := sqliteQueryString(backupDB, r.Context(), "SELECT version FROM schema_migrations ORDER BY version DESC LIMIT 1")
	ok := backupChecksOK(integrity, quick, schema, migration, size)
	h.audit.Log(r.Context(), audit.Entry{
		EventType: "system.backup_verified", ActorUserID: callerUserID(r),
		TargetType: "system", TargetID: filepath.Base(dbPath),
		IPAddress: h.clientIP(r), UserAgent: r.UserAgent(),
		Metadata: map[string]any{"ok": ok, "size_bytes": size, "sha256": hash, "schema_check": schema},
	})
	response.JSON(w, http.StatusOK, backupVerifyDTO{
		OK:             ok,
		IntegrityCheck: integrity,
		QuickCheck:     quick,
		SchemaCheck:    schema,
		SizeBytes:      size,
		SHA256:         hash,
		GeneratedAt:    time.Now().UTC().Format(time.RFC3339),
		Migration:      migration,
	})
}

func (h *Handler) listStoredBackups(w http.ResponseWriter, r *http.Request) {
	dir, err := h.backupDir(r.Context())
	if err != nil {
		response.Error(w, http.StatusInternalServerError, "backup_unavailable", err.Error())
		return
	}
	backups, err := readStoredBackups(dir)
	if err != nil {
		response.Error(w, http.StatusInternalServerError, "backup_unavailable", err.Error())
		return
	}
	response.JSON(w, http.StatusOK, map[string]any{"backups": backups, "retention": storedBackupRetention})
}

func (h *Handler) createStoredBackup(w http.ResponseWriter, r *http.Request) {
	dbPath, err := h.databasePath(r.Context())
	if err != nil || dbPath == "" {
		response.Error(w, http.StatusInternalServerError, "backup_unavailable", "database file path is unavailable")
		return
	}
	dir, err := h.backupDir(r.Context())
	if err != nil {
		response.Error(w, http.StatusInternalServerError, "backup_unavailable", err.Error())
		return
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		response.Error(w, http.StatusInternalServerError, "backup_failed", err.Error())
		return
	}
	name := "airbrew-backup-" + time.Now().UTC().Format("20060102T150405Z") + ".sqlite"
	path := filepath.Join(dir, name)
	escaped := strings.ReplaceAll(path, "'", "''")
	if _, err := h.db.ExecContext(r.Context(), "VACUUM INTO '"+escaped+"'"); err != nil {
		response.Error(w, http.StatusInternalServerError, "backup_failed", err.Error())
		return
	}
	if err := pruneStoredBackups(dir, storedBackupRetention); err != nil {
		response.Error(w, http.StatusInternalServerError, "backup_failed", err.Error())
		return
	}
	info, err := os.Stat(path)
	if err != nil {
		response.Error(w, http.StatusInternalServerError, "backup_failed", err.Error())
		return
	}
	h.audit.Log(r.Context(), audit.Entry{
		EventType: "system.backup_created", ActorUserID: callerUserID(r),
		TargetType: "system_backup", TargetID: name,
		IPAddress: h.clientIP(r), UserAgent: r.UserAgent(),
		Metadata: map[string]any{"source": filepath.Base(dbPath), "size_bytes": info.Size()},
	})
	response.JSON(w, http.StatusCreated, storedBackupDTO{Name: name, SizeBytes: info.Size(), CreatedAt: info.ModTime().UTC()})
}

func (h *Handler) downloadStoredBackup(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !validStoredBackupName(name) {
		response.Error(w, http.StatusBadRequest, "invalid_request", "invalid backup name")
		return
	}
	dir, err := h.backupDir(r.Context())
	if err != nil {
		response.Error(w, http.StatusInternalServerError, "backup_unavailable", err.Error())
		return
	}
	path := filepath.Join(dir, name)
	if _, err := os.Stat(path); err != nil {
		response.Error(w, http.StatusNotFound, "not_found", "backup not found")
		return
	}
	h.audit.Log(r.Context(), audit.Entry{
		EventType: "system.backup_downloaded", ActorUserID: callerUserID(r),
		TargetType: "system_backup", TargetID: name,
		IPAddress: h.clientIP(r), UserAgent: r.UserAgent(),
	})
	w.Header().Set("Content-Type", "application/vnd.sqlite3")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	http.ServeFile(w, r, path)
}

func (h *Handler) deleteStoredBackup(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !validStoredBackupName(name) {
		response.Error(w, http.StatusBadRequest, "invalid_request", "invalid backup name")
		return
	}
	dir, err := h.backupDir(r.Context())
	if err != nil {
		response.Error(w, http.StatusInternalServerError, "backup_unavailable", err.Error())
		return
	}
	path := filepath.Join(dir, name)
	if err := os.Remove(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			response.Error(w, http.StatusNotFound, "not_found", "backup not found")
			return
		}
		response.Error(w, http.StatusInternalServerError, "backup_failed", err.Error())
		return
	}
	h.audit.Log(r.Context(), audit.Entry{
		EventType: "system.backup_deleted", ActorUserID: callerUserID(r),
		TargetType: "system_backup", TargetID: name,
		IPAddress: h.clientIP(r), UserAgent: r.UserAgent(),
	})
	response.JSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (h *Handler) restoreDryRun(w http.ResponseWriter, r *http.Request) {
	tmpPath, _, cleanup, err := h.receiveBackupUpload(w, r, "airbrew-restore-dry-run-*.sqlite")
	if err != nil {
		return
	}
	defer cleanup()

	result, err := verifySQLiteFile(r.Context(), tmpPath)
	if err != nil {
		response.Error(w, http.StatusBadRequest, "backup_failed", err.Error())
		return
	}
	h.audit.Log(r.Context(), audit.Entry{
		EventType: "system.backup_restore_dry_run", ActorUserID: callerUserID(r),
		TargetType: "system", TargetID: "uploaded_backup",
		IPAddress: h.clientIP(r), UserAgent: r.UserAgent(),
		Metadata: map[string]any{"ok": result.OK, "size_bytes": result.SizeBytes, "sha256": result.SHA256},
	})
	response.JSON(w, http.StatusOK, result)
}

func (h *Handler) stageRestore(w http.ResponseWriter, r *http.Request) {
	tmpPath, fileName, cleanup, err := h.receiveBackupUpload(w, r, "airbrew-restore-stage-*.sqlite")
	if err != nil {
		return
	}
	defer cleanup()
	result, err := verifySQLiteFile(r.Context(), tmpPath)
	if err != nil {
		response.Error(w, http.StatusBadRequest, "backup_failed", err.Error())
		return
	}
	if !result.OK {
		response.Error(w, http.StatusBadRequest, "backup_failed", "backup failed integrity or Airbrew schema checks")
		return
	}
	dir, err := h.backupDir(r.Context())
	if err != nil {
		response.Error(w, http.StatusInternalServerError, "backup_unavailable", err.Error())
		return
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		response.Error(w, http.StatusInternalServerError, "backup_failed", err.Error())
		return
	}
	pendingPath := filepath.Join(dir, "restore-pending.sqlite")
	manifestPath := filepath.Join(dir, "restore-pending.json")
	if err := copyFile(tmpPath, pendingPath, 0o600); err != nil {
		response.Error(w, http.StatusInternalServerError, "backup_failed", err.Error())
		return
	}
	stagedAt := time.Now().UTC().Format(time.RFC3339)
	manifest := map[string]any{
		"staged_at":         stagedAt,
		"original_filename": fileName,
		"pending_path":      pendingPath,
		"sha256":            result.SHA256,
		"size_bytes":        result.SizeBytes,
		"migration_version": result.Migration,
	}
	b, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		response.Error(w, http.StatusInternalServerError, "backup_failed", err.Error())
		return
	}
	if err := os.WriteFile(manifestPath, b, 0o600); err != nil {
		response.Error(w, http.StatusInternalServerError, "backup_failed", err.Error())
		return
	}
	h.audit.Log(r.Context(), audit.Entry{
		EventType: "system.backup_restore_staged", ActorUserID: callerUserID(r),
		TargetType: "system_backup", TargetID: "restore-pending.sqlite",
		IPAddress: h.clientIP(r), UserAgent: r.UserAgent(),
		Metadata: map[string]any{"size_bytes": result.SizeBytes, "sha256": result.SHA256, "migration_version": result.Migration},
	})
	response.JSON(w, http.StatusCreated, restoreStageDTO{
		OK:             true,
		PendingPath:    pendingPath,
		ManifestPath:   manifestPath,
		StagedAt:       stagedAt,
		Verification:   result,
		RestartMessage: "Stop Airbrew, replace the current database with restore-pending.sqlite, then restart Airbrew.",
	})
}

func (h *Handler) receiveBackupUpload(w http.ResponseWriter, r *http.Request, pattern string) (string, string, func(), error) {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<30)
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		response.Error(w, http.StatusBadRequest, "invalid_request", err.Error())
		return "", "", func() {}, err
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		response.Error(w, http.StatusBadRequest, "invalid_request", "backup file is required")
		return "", "", func() {}, err
	}
	defer file.Close()
	tmp, err := os.CreateTemp("", pattern)
	if err != nil {
		response.Error(w, http.StatusInternalServerError, "internal_error", err.Error())
		return "", "", func() {}, err
	}
	tmpPath := tmp.Name()
	if _, err := io.Copy(tmp, file); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		response.Error(w, http.StatusInternalServerError, "backup_failed", err.Error())
		return "", "", func() {}, err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		response.Error(w, http.StatusInternalServerError, "backup_failed", err.Error())
		return "", "", func() {}, err
	}
	cleanup := func() { _ = os.Remove(tmpPath) }
	fileName := ""
	if header != nil {
		fileName = header.Filename
	}
	return tmpPath, fileName, cleanup, nil
}

func copyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

func fileSHA256(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

func sqlitePragmaString(db *sql.DB, ctx context.Context, query string) string {
	return sqliteQueryString(db, ctx, query)
}

func sqliteQueryString(db *sql.DB, ctx context.Context, query string) string {
	var out string
	if err := db.QueryRowContext(ctx, query).Scan(&out); err != nil {
		return err.Error()
	}
	return out
}

func verifySQLiteFile(ctx context.Context, path string) (backupVerifyDTO, error) {
	hash, size, err := fileSHA256(path)
	if err != nil {
		return backupVerifyDTO{}, err
	}
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro&_time_format=sqlite")
	if err != nil {
		return backupVerifyDTO{}, err
	}
	defer db.Close()
	integrity := sqlitePragmaString(db, ctx, "PRAGMA integrity_check")
	quick := sqlitePragmaString(db, ctx, "PRAGMA quick_check")
	schema := airbrewSchemaCheck(db, ctx)
	migration := sqliteQueryString(db, ctx, "SELECT version FROM schema_migrations ORDER BY version DESC LIMIT 1")
	return backupVerifyDTO{
		OK:             backupChecksOK(integrity, quick, schema, migration, size),
		IntegrityCheck: integrity,
		QuickCheck:     quick,
		SchemaCheck:    schema,
		SizeBytes:      size,
		SHA256:         hash,
		GeneratedAt:    time.Now().UTC().Format(time.RFC3339),
		Migration:      migration,
	}, nil
}

func backupChecksOK(integrity, quick, schema, migration string, size int64) bool {
	return integrity == "ok" && quick == "ok" && schema == "ok" && strings.TrimSpace(migration) != "" && size > 0
}

func airbrewSchemaCheck(db *sql.DB, ctx context.Context) string {
	required := []string{"schema_migrations", "users", "sessions", "audit_logs", "module_states", "server_settings"}
	missing := make([]string, 0)
	for _, table := range required {
		var n int
		err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&n)
		if err != nil {
			return err.Error()
		}
		if n == 0 {
			missing = append(missing, table)
		}
	}
	if len(missing) > 0 {
		return "missing tables: " + strings.Join(missing, ", ")
	}
	return "ok"
}

func (h *Handler) databasePath(ctx context.Context) (string, error) {
	rows, err := h.db.QueryContext(ctx, "PRAGMA database_list")
	if err != nil {
		return "", err
	}
	defer rows.Close()
	for rows.Next() {
		var seq int
		var name, file string
		if err := rows.Scan(&seq, &name, &file); err != nil {
			return "", err
		}
		if name == "main" {
			return file, nil
		}
	}
	return "", rows.Err()
}

func (h *Handler) backupDir(ctx context.Context) (string, error) {
	dbPath, err := h.databasePath(ctx)
	if err != nil {
		return "", err
	}
	if dbPath == "" {
		return "", errors.New("database file path is unavailable")
	}
	return filepath.Join(filepath.Dir(dbPath), "backups"), nil
}

func readStoredBackups(dir string) ([]storedBackupDTO, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []storedBackupDTO{}, nil
		}
		return nil, err
	}
	out := make([]storedBackupDTO, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !validStoredBackupName(name) {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return nil, err
		}
		out = append(out, storedBackupDTO{Name: name, SizeBytes: info.Size(), CreatedAt: info.ModTime().UTC()})
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].CreatedAt.After(out[j].CreatedAt)
	})
	return out, nil
}

func pruneStoredBackups(dir string, keep int) error {
	backups, err := readStoredBackups(dir)
	if err != nil {
		return err
	}
	for i := keep; i < len(backups); i++ {
		if err := os.Remove(filepath.Join(dir, backups[i].Name)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

func validStoredBackupName(name string) bool {
	if filepath.Base(name) != name {
		return false
	}
	if !strings.HasPrefix(name, "airbrew-backup-") || !strings.HasSuffix(name, ".sqlite") {
		return false
	}
	stamp := strings.TrimSuffix(strings.TrimPrefix(name, "airbrew-backup-"), ".sqlite")
	_, err := time.Parse("20060102T150405Z", stamp)
	return err == nil
}

func (h *Handler) latestMigrationVersion(ctx context.Context) (string, error) {
	var version string
	err := h.db.QueryRowContext(ctx,
		"SELECT version FROM schema_migrations ORDER BY version DESC LIMIT 1",
	).Scan(&version)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return version, err
}

// ---- Security: password policy ----

func (h *Handler) getPasswordPolicy(w http.ResponseWriter, r *http.Request) {
	p, err := h.security.PasswordPolicy(r.Context())
	if err != nil {
		response.Error(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	response.JSON(w, http.StatusOK, p)
}

type putPasswordPolicyReq struct {
	MinLength        *int  `json:"min_length,omitempty"`
	RequireUppercase *bool `json:"require_uppercase,omitempty"`
	RequireLowercase *bool `json:"require_lowercase,omitempty"`
	RequireDigit     *bool `json:"require_digit,omitempty"`
	RequireSymbol    *bool `json:"require_symbol,omitempty"`
	MaxAgeDays       *int  `json:"max_age_days,omitempty"`
	HistoryCount     *int  `json:"history_count,omitempty"`
}

func (h *Handler) putPasswordPolicy(w http.ResponseWriter, r *http.Request) {
	current, err := h.security.PasswordPolicy(r.Context())
	if err != nil {
		response.Error(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	var req putPasswordPolicyReq
	if err := decodeJSON(r, &req); err != nil {
		response.Error(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if req.MinLength != nil {
		current.MinLength = *req.MinLength
	}
	if req.RequireUppercase != nil {
		current.RequireUppercase = *req.RequireUppercase
	}
	if req.RequireLowercase != nil {
		current.RequireLowercase = *req.RequireLowercase
	}
	if req.RequireDigit != nil {
		current.RequireDigit = *req.RequireDigit
	}
	if req.RequireSymbol != nil {
		current.RequireSymbol = *req.RequireSymbol
	}
	if req.MaxAgeDays != nil {
		current.MaxAgeDays = *req.MaxAgeDays
	}
	if req.HistoryCount != nil {
		current.HistoryCount = *req.HistoryCount
	}
	if err := h.security.SetPasswordPolicy(r.Context(), current); err != nil {
		response.Error(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	h.audit.Log(r.Context(), audit.Entry{
		EventType: "security.password_policy_changed", ActorUserID: callerUserID(r),
		IPAddress: h.clientIP(r), UserAgent: r.UserAgent(),
		Metadata: map[string]any{
			"min_length": current.MinLength,
		},
	})
	normalized, _ := h.security.PasswordPolicy(r.Context())
	response.JSON(w, http.StatusOK, normalized)
}

// ---- Security: IP allowlist ----

func (h *Handler) getIPAllowlist(w http.ResponseWriter, r *http.Request) {
	a, err := h.security.IPAllowlist(r.Context())
	if err != nil {
		response.Error(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	response.JSON(w, http.StatusOK, a)
}

type putIPAllowlistReq struct {
	Enabled        *bool    `json:"enabled,omitempty"`
	CIDRs          []string `json:"cidrs,omitempty"`
	TrustedProxies []string `json:"trusted_proxies,omitempty"`
}

func (h *Handler) putIPAllowlist(w http.ResponseWriter, r *http.Request) {
	current, err := h.security.IPAllowlist(r.Context())
	if err != nil {
		response.Error(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	var req putIPAllowlistReq
	if err := decodeJSON(r, &req); err != nil {
		response.Error(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	for _, c := range req.CIDRs {
		if err := validateCIDR(c); err != nil {
			response.Error(w, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
	}
	for _, c := range req.TrustedProxies {
		if err := validateCIDR(c); err != nil {
			response.Error(w, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
	}
	if req.Enabled != nil {
		current.Enabled = *req.Enabled
	}
	if req.CIDRs != nil {
		current.CIDRs = req.CIDRs
	}
	if req.TrustedProxies != nil {
		current.TrustedProxies = req.TrustedProxies
	}
	requestIP := security.ResolveRequestIP(r, current)
	if current.Enabled && !security.IPMatches(requestIP, current.CIDRs) {
		response.Error(w, http.StatusBadRequest, "would_lock_out_current_ip", "current request IP must be included before enabling the allowlist")
		return
	}
	if err := h.security.SetIPAllowlist(r.Context(), current); err != nil {
		response.Error(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	h.audit.Log(r.Context(), audit.Entry{
		EventType: "security.ip_allowlist_changed", ActorUserID: callerUserID(r),
		IPAddress: requestIP, UserAgent: r.UserAgent(),
		Metadata: map[string]any{
			"enabled": current.Enabled, "count": len(current.CIDRs),
			"trusted_proxies": len(current.TrustedProxies),
		},
	})
	saved, _ := h.security.IPAllowlist(r.Context())
	response.JSON(w, http.StatusOK, saved)
}

// ---- Security: login history ----

func (h *Handler) loginHistory(w http.ResponseWriter, r *http.Request) {
	f := loginHistoryFilterFromRequest(r)
	rows, total, err := h.security.ListLoginAttempts(r.Context(), f)
	if err != nil {
		response.Error(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	response.JSON(w, http.StatusOK, map[string]any{
		"entries": rows, "total": total, "limit": f.Limit, "offset": f.Offset,
	})
}

func (h *Handler) exportLoginHistory(w http.ResponseWriter, r *http.Request) {
	f := loginHistoryFilterFromRequest(r)
	if f.To.IsZero() {
		f.To = time.Now().UTC()
	}
	total, err := h.writeLoginHistoryCSV(w, r, f)
	if err != nil {
		return
	}
	h.audit.Log(context.Background(), audit.Entry{
		EventType: "security.login_history_exported", ActorUserID: callerUserID(r),
		TargetType: "login_history", TargetID: "csv",
		IPAddress: h.clientIP(r), UserAgent: r.UserAgent(),
		Metadata: map[string]any{"rows": total, "to": f.To.Format(time.RFC3339)},
	})
}

func loginHistoryFilterFromRequest(r *http.Request) security.LoginHistoryFilter {
	q := r.URL.Query()
	f := security.LoginHistoryFilter{
		Email:  q.Get("email"),
		UserID: q.Get("user_id"),
		Only:   q.Get("result"),
	}
	if v := q.Get("from"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			f.From = t
		}
	}
	if v := q.Get("to"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			f.To = t
		}
	}
	f.Limit, _ = strconv.Atoi(q.Get("limit"))
	f.Offset, _ = strconv.Atoi(q.Get("offset"))
	return f
}

func (h *Handler) writeLoginHistoryCSV(w http.ResponseWriter, r *http.Request, f security.LoginHistoryFilter) (int, error) {
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="airbrew-login-history.csv"`)
	cw := csv.NewWriter(w)
	if err := cw.Write([]string{"created_at", "success", "email", "user_id", "ip_address", "user_agent", "failure"}); err != nil {
		return 0, err
	}
	exported := 0
	for offset := 0; ; {
		f.Limit = 200
		f.Offset = offset
		entries, _, err := h.security.ListLoginAttempts(r.Context(), f)
		if err != nil {
			return exported, err
		}
		for _, e := range entries {
			if err := cw.Write([]string{
				e.CreatedAt.UTC().Format(time.RFC3339),
				strconv.FormatBool(e.Success),
				e.Email,
				e.UserID,
				e.IPAddress,
				e.UserAgent,
				e.Failure,
			}); err != nil {
				return exported, err
			}
			exported++
		}
		if len(entries) == 0 {
			break
		}
		offset += len(entries)
	}
	cw.Flush()
	return exported, cw.Error()
}
