// Package response provides JSON helpers for HTTP handlers.
package response

import (
	"encoding/json"
	"net/http"
)

// ErrorBody matches the OAuth 2.1 error response shape.
type ErrorBody struct {
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description,omitempty"`
}

// JSON writes v as JSON with the given status code.
func JSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// Error writes an ErrorBody with code/description.
func Error(w http.ResponseWriter, status int, code, description string) {
	JSON(w, status, ErrorBody{Error: code, ErrorDescription: description})
}
