package handler

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/headercat/airbrew/internal/auth/session"
	"github.com/headercat/airbrew/internal/drive/files"
)

const timeRFC3339 = "2006-01-02T15:04:05Z07:00"

func requireSession(w http.ResponseWriter, r *http.Request) (*session.Session, bool) {
	sess, ok := session.FromContext(r.Context())
	if !ok {
		respondErr(w, http.StatusUnauthorized, "unauthorized", "no active session")
		return nil, false
	}
	return sess, true
}

func decodeJSON(r *http.Request, v any) error {
	ct := r.Header.Get("Content-Type")
	if !strings.Contains(ct, "application/json") {
		return errors.New("content-type must be application/json")
	}
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}

func jsonResp(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func respondErr(w http.ResponseWriter, status int, code, desc string) {
	jsonResp(w, status, map[string]string{"error": code, "error_description": desc})
}

// writeErr maps a service error to the matching HTTP status.
func writeErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, files.ErrNotFound), errors.Is(err, files.ErrFolderNotFound),
		errors.Is(err, files.ErrShareNotFound):
		respondErr(w, http.StatusNotFound, "not_found", err.Error())
	case errors.Is(err, files.ErrExpired):
		respondErr(w, http.StatusGone, "expired", err.Error())
	case errors.Is(err, files.ErrPasswordRequired):
		respondErr(w, http.StatusUnauthorized, "password_required", err.Error())
	case errors.Is(err, files.ErrCircularMove):
		respondErr(w, http.StatusConflict, "circular_move", err.Error())
	case errors.Is(err, files.ErrQuotaExceeded):
		respondErr(w, http.StatusInsufficientStorage, "quota_exceeded", err.Error())
	case errors.Is(err, files.ErrTooLarge):
		respondErr(w, http.StatusRequestEntityTooLarge, "too_large", err.Error())
	case errors.Is(err, files.ErrInvalidInput), errors.Is(err, files.ErrNameRequired):
		respondErr(w, http.StatusBadRequest, "invalid_request", err.Error())
	default:
		respondErr(w, http.StatusInternalServerError, "internal_error", err.Error())
	}
}

func parseInt(s string) int {
	n, _ := strconv.Atoi(strings.TrimSpace(s))
	return n
}

func parseBool(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

func clientIP(r *http.Request) string {
	if f := r.Header.Get("X-Forwarded-For"); f != "" {
		if i := strings.Index(f, ","); i > 0 {
			return strings.TrimSpace(f[:i])
		}
		return strings.TrimSpace(f)
	}
	return r.RemoteAddr
}
