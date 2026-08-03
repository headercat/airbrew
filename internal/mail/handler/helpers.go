package handler

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/headercat/airbrew/internal/auth/session"
	"github.com/headercat/airbrew/internal/httpserver/requestip"
	"github.com/headercat/airbrew/internal/mail/inbox"
	"github.com/headercat/airbrew/internal/mail/provider"
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
	// Cap the body so a caller cannot OOM the server by pasting a huge
	// base64 blob into a JSON field. Attachment uploads go through a
	// separate multipart path with their own 25 MiB cap.
	r.Body = http.MaxBytesReader(nil, r.Body, 4<<20)
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
	case errors.Is(err, inbox.ErrMailboxNotFound), errors.Is(err, inbox.ErrMessageNotFound),
		errors.Is(err, provider.ErrNotFound):
		respondErr(w, http.StatusNotFound, "not_found", err.Error())
	case errors.Is(err, inbox.ErrAddressTaken):
		respondErr(w, http.StatusConflict, "address_taken", err.Error())
	case errors.Is(err, inbox.ErrDuplicate):
		respondErr(w, http.StatusConflict, "duplicate", err.Error())
	case errors.Is(err, inbox.ErrInvalidInput), errors.Is(err, provider.ErrInvalidInput):
		respondErr(w, http.StatusBadRequest, "invalid_request", err.Error())
	case errors.Is(err, inbox.ErrNoOutbound), errors.Is(err, provider.ErrNoActive):
		respondErr(w, http.StatusServiceUnavailable, "no_provider", err.Error())
	default:
		respondErr(w, http.StatusInternalServerError, "internal_error", err.Error())
	}
}

func parseInt(s string) int {
	n, _ := strconv.Atoi(strings.TrimSpace(s))
	return n
}

func clientIP(r *http.Request) string {
	return requestip.DirectClientIP(r)
}
