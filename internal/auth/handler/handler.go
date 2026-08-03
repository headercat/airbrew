// Package handler exposes the HTTP endpoints for the auth module.
package handler

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/headercat/airbrew/internal/audit"
	"github.com/headercat/airbrew/internal/auth/session"
	"github.com/headercat/airbrew/internal/auth/user"
	"github.com/headercat/airbrew/internal/blob"
	"github.com/headercat/airbrew/internal/httpserver/middleware"
	"github.com/headercat/airbrew/internal/httpserver/response"
	"github.com/headercat/airbrew/internal/security"
)

// Handler exposes the auth JSON endpoints.
type Handler struct {
	users           *user.Repository
	userSvc         *user.Service
	sessions        *session.Service
	blobs           blob.Store
	audit           *audit.Service
	security        *security.Service
	cookieSecure    bool
	registerLimiter *middleware.RateLimiter
}

// Deps wires handler dependencies.
type Deps struct {
	UserRepo *user.Repository
	UserSvc  *user.Service
	SessSvc  *session.Service
	Blobs    blob.Store
	Audit    *audit.Service
	Security *security.Service
	// CookieSecure, when true, marks the session cookie with the Secure
	// attribute so it is only ever sent over HTTPS. Required for production;
	// false is fine for local HTTP dev.
	CookieSecure bool
}

// New builds a Handler.
func New(d Deps) *Handler {
	return &Handler{
		users: d.UserRepo, userSvc: d.UserSvc, sessions: d.SessSvc,
		blobs: d.Blobs, audit: d.Audit, security: d.Security, cookieSecure: d.CookieSecure,
		// Registration runs argon2id (CPU-heavy) and is unauthenticated, so cap
		// it per IP to blunt account-creation / enumeration / hashing DoS.
		registerLimiter: middleware.NewRateLimiter(10, time.Minute),
	}
}

// RegisterRoutes mounts the JSON endpoints on mux. All routes are under /api/auth.
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/auth/register", h.register)
	mux.HandleFunc("POST /api/auth/login", h.login)
	mux.HandleFunc("POST /api/auth/logout", h.logout)
	mux.HandleFunc("GET /api/auth/me", h.me)
	mux.HandleFunc("PATCH /api/auth/me", h.updateProfile)
	mux.HandleFunc("POST /api/auth/password", h.changePassword)
	mux.HandleFunc("POST /api/auth/password/expired", h.changeExpiredPassword)
	mux.HandleFunc("POST /api/auth/avatar", h.uploadAvatar)
}

type registerReq struct {
	Email       string `json:"email"`
	Password    string `json:"password"`
	DisplayName string `json:"display_name,omitempty"`
}

type userResp struct {
	ID           string            `json:"id"`
	Email        string            `json:"email"`
	DisplayName  string            `json:"display_name"`
	Status       string            `json:"status"`
	Role         string            `json:"role"`
	Description  string            `json:"description"`
	Birthday     string            `json:"birthday"`
	PhoneNumber  string            `json:"phone_number"`
	AvatarURL    string            `json:"avatar_url"`
	CustomFields map[string]string `json:"custom_fields"`
}

func toResp(u *user.User) userResp {
	r := userResp{
		ID:           u.ID,
		Email:        u.Email,
		DisplayName:  u.DisplayName,
		Status:       string(u.Status),
		Role:         string(u.Role),
		Description:  u.Description,
		PhoneNumber:  u.PhoneNumber,
		AvatarURL:    u.AvatarURL,
		CustomFields: u.CustomFields,
	}
	if u.Birthday != nil {
		r.Birthday = u.Birthday.Format("2006-01-02")
	}
	if r.CustomFields == nil {
		r.CustomFields = map[string]string{}
	}
	return r
}

func (h *Handler) register(w http.ResponseWriter, r *http.Request) {
	if h.registerLimiter != nil && !h.registerLimiter.Allow(h.clientIP(r)) {
		w.Header().Set("Retry-After", "60")
		response.Error(w, http.StatusTooManyRequests, "rate_limited", "too many registration attempts")
		return
	}
	var req registerReq
	if err := decodeJSON(r, &req); err != nil {
		response.Error(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	u, err := h.userSvc.Register(r.Context(), req.Email, req.Password, req.DisplayName)
	if err != nil {
		if errors.Is(err, user.ErrEmailTaken) {
			response.Error(w, http.StatusConflict, "email_taken", "email already registered")
			return
		}
		if errors.Is(err, user.ErrInvalidEmail) {
			response.Error(w, http.StatusBadRequest, "invalid_email", "enter a valid email address")
			return
		}
		response.Error(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	h.audit.Log(r.Context(), audit.Entry{
		EventType:   "user.created",
		ActorUserID: u.ID,
		TargetType:  "user", TargetID: u.ID,
		IPAddress: h.clientIP(r),
		UserAgent: r.UserAgent(),
		Metadata:  map[string]any{"email": u.Email},
	})
	response.JSON(w, http.StatusCreated, toResp(u))
}

type loginReq struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

func (h *Handler) login(w http.ResponseWriter, r *http.Request) {
	var req loginReq
	if err := decodeJSON(r, &req); err != nil {
		response.Error(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	email := strings.TrimSpace(req.Email)
	if h.loginRateLimited(w, r, email) {
		return
	}
	u, err := h.userSvc.Authenticate(r.Context(), req.Email, req.Password)
	if err != nil {
		h.recordLoginAttempt(r, email, "", false, "invalid_credentials")
		response.Error(w, http.StatusUnauthorized, "invalid_credentials", "invalid email or password")
		return
	}
	expired, err := h.passwordExpired(r, u.ID)
	if err != nil {
		h.recordLoginAttempt(r, email, u.ID, false, "password_policy_check_failed")
		response.Error(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	if expired {
		h.recordLoginAttempt(r, email, u.ID, false, "password_expired")
		response.Error(w, http.StatusForbidden, "password_expired", "password has expired")
		return
	}
	token, _, err := h.sessions.Issue(r.Context(), u.ID, h.clientIP(r), r.UserAgent())
	if err != nil {
		h.recordLoginAttempt(r, email, u.ID, false, "session_issue_failed")
		response.Error(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	h.recordLoginAttempt(r, email, u.ID, true, "")
	h.setSessionCookie(w, token, h.sessions.MaxAge())
	h.audit.Log(r.Context(), audit.Entry{
		EventType:   "session.login",
		ActorUserID: u.ID,
		TargetType:  "user", TargetID: u.ID,
		IPAddress: h.clientIP(r),
		UserAgent: r.UserAgent(),
	})
	response.JSON(w, http.StatusOK, toResp(u))
}

func (h *Handler) passwordExpired(r *http.Request, userID string) (bool, error) {
	if h.security == nil {
		return false, nil
	}
	p, err := h.security.PasswordPolicy(r.Context())
	if err != nil {
		return false, err
	}
	if p.MaxAgeDays <= 0 {
		return false, nil
	}
	changed, err := h.users.PasswordChangedAt(r.Context(), userID)
	if err != nil {
		return false, err
	}
	if changed.IsZero() {
		return false, nil
	}
	return time.Since(changed) > time.Duration(p.MaxAgeDays)*24*time.Hour, nil
}

func (h *Handler) recordLoginAttempt(r *http.Request, email, userID string, success bool, failure string) {
	if h.security == nil {
		return
	}
	if userID == "" && email != "" {
		if u, err := h.users.GetByEmail(r.Context(), email); err == nil {
			userID = u.ID
		}
	}
	_ = h.security.RecordLoginAttempt(r.Context(), security.LoginAttempt{
		UserID:    userID,
		Email:     email,
		Success:   success,
		IPAddress: h.clientIP(r),
		UserAgent: r.UserAgent(),
		Failure:   failure,
	})
}

func (h *Handler) loginRateLimited(w http.ResponseWriter, r *http.Request, email string) bool {
	if h.security == nil {
		return false
	}
	limit, err := h.security.CheckLoginRateLimit(r.Context(), email, h.clientIP(r))
	if err != nil {
		response.Error(w, http.StatusInternalServerError, "internal_error", err.Error())
		return true
	}
	if !limit.Blocked {
		return false
	}
	retryAfter := int(time.Until(limit.RetryAfter).Seconds())
	if retryAfter < 1 {
		retryAfter = 1
	}
	w.Header().Set("Retry-After", strconv.Itoa(retryAfter))
	h.recordLoginAttempt(r, email, "", false, "rate_limited")
	response.Error(w, http.StatusTooManyRequests, "rate_limited", "too many failed login attempts")
	return true
}

func (h *Handler) logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(session.CookieName); err == nil && c.Value != "" {
		if sess, err := h.sessions.Lookup(r.Context(), c.Value); err == nil {
			_ = h.sessions.Revoke(r.Context(), sess.ID)
			h.audit.Log(r.Context(), audit.Entry{
				EventType:   "session.logout",
				ActorUserID: sess.UserID,
				TargetType:  "session", TargetID: sess.ID,
				IPAddress: h.clientIP(r),
				UserAgent: r.UserAgent(),
			})
		}
	}
	h.setSessionCookie(w, "", -1)
	response.JSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (h *Handler) me(w http.ResponseWriter, r *http.Request) {
	sess, ok := session.FromContext(r.Context())
	if !ok {
		response.Error(w, http.StatusUnauthorized, "unauthorized", "no active session")
		return
	}
	u, err := h.users.GetByID(r.Context(), sess.UserID)
	if err != nil {
		response.Error(w, http.StatusNotFound, "not_found", "user not found")
		return
	}
	response.JSON(w, http.StatusOK, toResp(u))
}

// updateProfileReq replaces all editable profile fields at once. Empty strings
// clear the corresponding column.
type updateProfileReq struct {
	DisplayName  string            `json:"display_name"`
	Description  string            `json:"description"`
	Birthday     string            `json:"birthday"` // YYYY-MM-DD or ""
	PhoneNumber  string            `json:"phone_number"`
	CustomFields map[string]string `json:"custom_fields"`
}

func (h *Handler) updateProfile(w http.ResponseWriter, r *http.Request) {
	sess, ok := session.FromContext(r.Context())
	if !ok {
		response.Error(w, http.StatusUnauthorized, "unauthorized", "no active session")
		return
	}
	var req updateProfileReq
	if err := decodeJSON(r, &req); err != nil {
		response.Error(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	upd := user.ProfileUpdate{
		DisplayName:  req.DisplayName,
		Description:  req.Description,
		PhoneNumber:  req.PhoneNumber,
		CustomFields: req.CustomFields,
	}
	if req.Birthday != "" {
		t, err := time.Parse("2006-01-02", req.Birthday)
		if err != nil {
			response.Error(w, http.StatusBadRequest, "invalid_request", "birthday must be YYYY-MM-DD")
			return
		}
		upd.Birthday = &t
	} else {
		// Empty string is an explicit clear.
		zero := time.Time{}
		upd.Birthday = &zero
	}
	if err := h.userSvc.UpdateProfile(r.Context(), sess.UserID, upd); err != nil {
		response.Error(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	u, err := h.users.GetByID(r.Context(), sess.UserID)
	if err != nil {
		response.Error(w, http.StatusNotFound, "not_found", "user not found")
		return
	}
	h.audit.Log(r.Context(), audit.Entry{
		EventType:   "profile.updated",
		ActorUserID: sess.UserID,
		TargetType:  "user", TargetID: sess.UserID,
		IPAddress: h.clientIP(r),
		UserAgent: r.UserAgent(),
	})
	response.JSON(w, http.StatusOK, toResp(u))
}

type changePasswordReq struct {
	CurrentPassword string `json:"current_password"`
	NewPassword     string `json:"new_password"`
}

type expiredPasswordReq struct {
	Email           string `json:"email"`
	CurrentPassword string `json:"current_password"`
	NewPassword     string `json:"new_password"`
}

func (h *Handler) changeExpiredPassword(w http.ResponseWriter, r *http.Request) {
	var req expiredPasswordReq
	if err := decodeJSON(r, &req); err != nil {
		response.Error(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	email := strings.TrimSpace(req.Email)
	if h.loginRateLimited(w, r, email) {
		return
	}
	u, err := h.userSvc.Authenticate(r.Context(), email, req.CurrentPassword)
	if err != nil {
		h.recordLoginAttempt(r, email, "", false, "invalid_credentials")
		response.Error(w, http.StatusUnauthorized, "invalid_credentials", "invalid email or password")
		return
	}
	if err := h.userSvc.ChangePassword(r.Context(), u.ID, req.CurrentPassword, req.NewPassword); err != nil {
		if errors.Is(err, user.ErrInvalidCredentials) {
			response.Error(w, http.StatusUnauthorized, "invalid_credentials", "current password is incorrect")
			return
		}
		response.Error(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	h.recordLoginAttempt(r, u.Email, u.ID, true, "")
	h.audit.Log(r.Context(), audit.Entry{
		EventType:   "user.password_changed",
		ActorUserID: u.ID,
		TargetType:  "user", TargetID: u.ID,
		IPAddress: h.clientIP(r),
		UserAgent: r.UserAgent(),
	})
	response.JSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (h *Handler) changePassword(w http.ResponseWriter, r *http.Request) {
	sess, ok := session.FromContext(r.Context())
	if !ok {
		response.Error(w, http.StatusUnauthorized, "unauthorized", "no active session")
		return
	}
	var req changePasswordReq
	if err := decodeJSON(r, &req); err != nil {
		response.Error(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if err := h.userSvc.ChangePassword(r.Context(), sess.UserID, req.CurrentPassword, req.NewPassword); err != nil {
		if errors.Is(err, user.ErrInvalidCredentials) {
			response.Error(w, http.StatusUnauthorized, "invalid_credentials", "current password is incorrect")
			return
		}
		response.Error(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	// Revoke every other session for this user so a compromised device that
	// still holds a valid cookie is forced out alongside the password change.
	revoked, _ := h.sessions.RevokeAllForUserExcept(r.Context(), sess.UserID, sess.ID)
	h.audit.Log(r.Context(), audit.Entry{
		EventType:   "user.password_changed",
		ActorUserID: sess.UserID,
		TargetType:  "user", TargetID: sess.UserID,
		IPAddress: h.clientIP(r),
		UserAgent: r.UserAgent(),
		Metadata:  map[string]any{"sessions_revoked": revoked},
	})
	response.JSON(w, http.StatusOK, map[string]any{"ok": true, "sessions_revoked": revoked})
}

const maxAvatarBytes = 4 << 20 // 4 MiB

var allowedAvatarTypes = map[string]bool{
	"image/png":  true,
	"image/jpeg": true,
	"image/webp": true,
	"image/gif":  true,
}

func (h *Handler) uploadAvatar(w http.ResponseWriter, r *http.Request) {
	sess, ok := session.FromContext(r.Context())
	if !ok {
		response.Error(w, http.StatusUnauthorized, "unauthorized", "no active session")
		return
	}
	if h.blobs == nil {
		response.Error(w, http.StatusServiceUnavailable, "unavailable", "blob store not configured")
		return
	}
	// Cap the whole request body (not just the in-memory threshold) so a
	// client cannot stream gigabytes of multipart onto temp disk.
	r.Body = http.MaxBytesReader(w, r.Body, maxAvatarBytes+512)
	if err := r.ParseMultipartForm(maxAvatarBytes); err != nil {
		response.Error(w, http.StatusBadRequest, "invalid_request", "failed to parse multipart: "+err.Error())
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		response.Error(w, http.StatusBadRequest, "invalid_request", "file field is required")
		return
	}
	defer file.Close()
	if header.Size > maxAvatarBytes {
		response.Error(w, http.StatusBadRequest, "invalid_request", "avatar must be 4 MiB or smaller")
		return
	}
	bufHead := make([]byte, 512)
	n, err := io.ReadFull(file, bufHead)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		response.Error(w, http.StatusBadRequest, "invalid_request", "could not read file")
		return
	}
	ct := http.DetectContentType(bufHead[:n])
	if !allowedAvatarTypes[ct] {
		response.Error(w, http.StatusBadRequest, "invalid_request", "avatar must be PNG, JPEG, WebP, or GIF")
		return
	}
	reader := io.MultiReader(strings.NewReader(string(bufHead[:n])), file)
	storedPath, err := h.blobs.Save(r.Context(), "avatars", ct, reader)
	if err != nil {
		response.Error(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	url := "/api/files/" + storedPath
	// Best-effort: delete the previous avatar blob so uploads do not leak
	// orphaned files in the avatars/ namespace on every change.
	if old := h.currentUserAvatarPath(r.Context(), sess.UserID); old != "" && old != storedPath {
		_ = h.blobs.Delete(r.Context(), old)
	}
	if err := h.userSvc.SetAvatarURL(r.Context(), sess.UserID, url); err != nil {
		response.Error(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	h.audit.Log(r.Context(), audit.Entry{
		EventType:   "avatar.uploaded",
		ActorUserID: sess.UserID,
		TargetType:  "user", TargetID: sess.UserID,
		IPAddress: h.clientIP(r), UserAgent: r.UserAgent(),
	})
	response.JSON(w, http.StatusOK, map[string]string{"avatar_url": url})
}

func decodeJSON(r *http.Request, v any) error {
	ct := r.Header.Get("Content-Type")
	if !strings.Contains(ct, "application/json") {
		return errors.New("content-type must be application/json")
	}
	// Cap the body so an unauthenticated register/login attempt cannot stream
	// an oversized payload into the JSON parser. 64 KiB is well above any
	// legitimate auth payload.
	r.Body = http.MaxBytesReader(nil, r.Body, 64<<10)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}

func (h *Handler) setSessionCookie(w http.ResponseWriter, value string, maxAge int) {
	http.SetCookie(w, &http.Cookie{
		Name:     session.CookieName,
		Value:    value,
		Path:     "/",
		HttpOnly: true,
		Secure:   h.cookieSecure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   maxAge,
	})
}

// currentUserAvatarPath returns the stored blob path (e.g. "avatars/<name>")
// for the user's current avatar, or "" if they have none. Used to delete the
// previous blob when a new avatar is uploaded.
func (h *Handler) currentUserAvatarPath(ctx context.Context, userID string) string {
	u, err := h.users.GetByID(ctx, userID)
	if err != nil || u == nil || u.AvatarURL == "" {
		return ""
	}
	const prefix = "/api/files/"
	if strings.HasPrefix(u.AvatarURL, prefix) {
		return strings.TrimPrefix(u.AvatarURL, prefix)
	}
	return ""
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
