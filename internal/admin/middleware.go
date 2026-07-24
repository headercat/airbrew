package admin

import (
	"net/http"

	"github.com/headercat/airbrew/internal/auth/session"
)

// sessionFromRequest pulls the session back out of context. Used to detect
// the caller for self-action guards.
func sessionFromRequest(r *http.Request) (*session.Session, bool) {
	return session.FromContext(r.Context())
}

// callerUserID returns the admin user's ID from the session context, or ""
// if no session is present (shouldn't happen behind RequireAdmin).
func callerUserID(r *http.Request) string {
	if sess, ok := sessionFromRequest(r); ok {
		return sess.UserID
	}
	return ""
}
