package handler

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/headercat/airbrew/internal/auth/session"
	"github.com/headercat/airbrew/internal/httpserver/requestip"
	"github.com/headercat/airbrew/internal/workflow/run"
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
	// Cap the request body so a malicious client cannot stream an unbounded
	// JSON payload into the parser. 1 MiB is well above any legitimate
	// workflow definition or contact payload.
	r.Body = http.MaxBytesReader(nil, r.Body, 1<<20)
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

func writeErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, run.ErrNotFound):
		respondErr(w, http.StatusNotFound, "not_found", err.Error())
	case errors.Is(err, run.ErrActiveConflict), errors.Is(err, run.ErrTokenTaken):
		respondErr(w, http.StatusConflict, "conflict", err.Error())
	case errors.Is(err, run.ErrInvalidInput), errors.Is(err, run.ErrDefinitionInvalid):
		respondErr(w, http.StatusBadRequest, "invalid_request", err.Error())
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
