package admin

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
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
	Key         string `json:"key"`
	Name        string `json:"name"`
	Description string `json:"description"`
	AdminOnly   bool   `json:"admin_only"`
	System      bool   `json:"system"`
	Enabled     bool   `json:"enabled"`
}

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
	adminCount, _ := h.userRepo.CountByRole(ctx, user.RoleAdmin)
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
	states, err := h.state.AllEnabled(r.Context())
	if err != nil {
		response.Error(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	out := make([]moduleDTO, 0, len(modules.Catalog))
	for _, m := range modules.Catalog {
		out = append(out, moduleDTO{
			Key: m.Key, Name: m.Name, Description: m.Description,
			AdminOnly: m.AdminOnly, System: m.System, Enabled: states[m.Key],
		})
	}
	response.JSON(w, http.StatusOK, map[string]any{"modules": out})
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
		IPAddress: clientIP(r), UserAgent: r.UserAgent(),
	})
	response.JSON(w, http.StatusOK, map[string]any{"key": key, "enabled": *req.Enabled})
}

// ---- Users ----

type userDTO struct {
	ID          string    `json:"id"`
	Email       string    `json:"email"`
	DisplayName string    `json:"display_name"`
	Status      string    `json:"status"`
	Role        string    `json:"role"`
	CreatedAt   time.Time `json:"created_at"`
}

func toUserDTO(u *user.User) userDTO {
	return userDTO{
		ID: u.ID, Email: u.Email, DisplayName: u.DisplayName,
		Status: string(u.Status), Role: string(u.Role), CreatedAt: u.CreatedAt,
	}
}

func (h *Handler) listUsers(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	search := q.Get("search")
	limit, _ := strconv.Atoi(q.Get("limit"))
	offset, _ := strconv.Atoi(q.Get("offset"))
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	users, err := h.userRepo.Search(r.Context(), search, limit, offset)
	if err != nil {
		response.Error(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	total, err := h.userRepo.Count(r.Context())
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
		IPAddress: clientIP(r), UserAgent: r.UserAgent(),
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
			count, err := h.userRepo.CountByRole(r.Context(), user.RoleAdmin)
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
			IPAddress: clientIP(r), UserAgent: r.UserAgent(),
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
			count, err := h.userRepo.CountByRole(r.Context(), user.RoleAdmin)
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
		h.audit.Log(r.Context(), audit.Entry{
			EventType: "user.status_changed", ActorUserID: callerID,
			TargetType: "user", TargetID: id,
			IPAddress: clientIP(r), UserAgent: r.UserAgent(),
			Metadata: map[string]any{"from": string(target.Status), "to": string(newStatus)},
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
		count, err := h.userRepo.CountByRole(r.Context(), user.RoleAdmin)
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
	h.audit.Log(r.Context(), audit.Entry{
		EventType: "user.deleted", ActorUserID: callerID,
		TargetType: "user", TargetID: id,
		IPAddress: clientIP(r), UserAgent: r.UserAgent(),
		Metadata: map[string]any{"from": string(target.Status), "to": string(user.StatusDeleted)},
	})
	response.JSON(w, http.StatusOK, map[string]bool{"ok": true})
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
	h.audit.Log(r.Context(), audit.Entry{
		EventType: "user.password_reset", ActorUserID: callerUserID(r),
		TargetType: "user", TargetID: id,
		IPAddress: clientIP(r), UserAgent: r.UserAgent(),
	})
	resp := map[string]any{"ok": true}
	if generated {
		resp["temp_password"] = pw
	}
	response.JSON(w, http.StatusOK, resp)
}

// ---- Audit Log ----

func (h *Handler) listAudit(w http.ResponseWriter, r *http.Request) {
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
		IPAddress: clientIP(r), UserAgent: r.UserAgent(),
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
	h.audit.Log(r.Context(), audit.Entry{
		EventType: "oauth.client_updated", ActorUserID: callerUserID(r),
		TargetType: "oauth_client", TargetID: c.ID,
		IPAddress: clientIP(r), UserAgent: r.UserAgent(),
		Metadata: map[string]any{"client_id": c.ClientID, "name": c.Name, "is_active": c.IsActive},
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
	h.audit.Log(r.Context(), audit.Entry{
		EventType: "oauth.client_deleted", ActorUserID: callerUserID(r),
		TargetType: "oauth_client", TargetID: id,
		IPAddress: clientIP(r), UserAgent: r.UserAgent(),
		Metadata: map[string]any{"client_id": c.ClientID, "name": c.Name},
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
	h.audit.Log(r.Context(), audit.Entry{
		EventType: "oauth.client_secret_rotated", ActorUserID: callerUserID(r),
		TargetType: "oauth_client", TargetID: id,
		IPAddress: clientIP(r), UserAgent: r.UserAgent(),
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
	if f := r.Header.Get("X-Forwarded-For"); f != "" {
		parts := strings.SplitN(f, ",", 2)
		return strings.TrimSpace(parts[0])
	}
	return r.RemoteAddr
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
		IPAddress: clientIP(r), UserAgent: r.UserAgent(),
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
	now := time.Now().UTC().Truncate(time.Second)
	res, err := h.db.ExecContext(r.Context(), `
		UPDATE sessions
		SET revoked_at = ?
		WHERE user_id = ? AND revoked_at IS NULL AND expires_at > ?
	`, now, id, now)
	if err != nil {
		response.Error(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	n, _ := res.RowsAffected()
	h.audit.Log(r.Context(), audit.Entry{
		EventType: "session.revoked", ActorUserID: callerUserID(r),
		TargetType: "user", TargetID: id,
		IPAddress: clientIP(r), UserAgent: r.UserAgent(),
		Metadata: map[string]any{"count": n},
	})
	response.JSON(w, http.StatusOK, map[string]any{"ok": true, "revoked": n})
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
		h.setSetting(r.Context(), "branding.logo_url", strings.TrimSpace(*req.LogoURL))
	}
	if req.PrimaryColor != nil {
		v := strings.TrimSpace(*req.PrimaryColor)
		h.setSetting(r.Context(), "branding.primary_color", v)
	}
	h.audit.Log(r.Context(), audit.Entry{
		EventType: "branding.updated", ActorUserID: callerUserID(r),
		IPAddress: clientIP(r), UserAgent: r.UserAgent(),
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

func (h *Handler) setSetting(ctx context.Context, key, value string) {
	now := time.Now().UTC().Truncate(time.Second)
	_, _ = h.db.ExecContext(ctx, `
		INSERT INTO server_settings (key, value, updated_at) VALUES (?, ?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at
	`, key, value, now)
}

// ---- System ----

type systemDTO struct {
	Version       string `json:"version"`
	GoVersion     string `json:"go_version"`
	NumCPU        int    `json:"num_cpu"`
	UserCount     int    `json:"user_count"`
	ModuleEnabled int    `json:"modules_enabled"`
	ModuleTotal   int    `json:"modules_total"`
	SessionCount  int    `json:"active_sessions"`
	DBPath        string `json:"db_path"`
	DBSizeMB      string `json:"db_size_mb"`
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
	var dbPath string
	dbSize := "unknown"
	if row := h.db.QueryRowContext(ctx, "PRAGMA database_list"); true {
		var id, file string
		_ = row.Scan(&id, &file, &dbPath)
		if fi, err := os.Stat(dbPath); err == nil {
			dbSize = fmt.Sprintf("%.1f", float64(fi.Size())/1024/1024)
		}
	}

	response.JSON(w, http.StatusOK, systemDTO{
		Version:       "0.1.0",
		GoVersion:     runtime.Version(),
		NumCPU:        runtime.NumCPU(),
		UserCount:     userCount,
		ModuleEnabled: enabled,
		ModuleTotal:   len(modules.Catalog),
		SessionCount:  activeSessions,
		DBPath:        filepath.Base(dbPath),
		DBSizeMB:      dbSize,
	})
}
