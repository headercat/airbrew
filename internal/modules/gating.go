// Package modules — gating.go
//
// RequireEnabled is middleware that 503s a request when the named module is
// disabled in server_settings. It lets an admin actually take a feature
// offline — without it, the /api/<name>/status endpoint would report
// "disabled" while the real endpoints kept working.
package modules

import (
	"net/http"
)

// RequireEnabled returns middleware that rejects requests when key is disabled.
// On error (e.g. DB unavailable) it fails open (allows) so a transient blip
// cannot take the whole feature down; the status endpoint still reports the
// authoritative state.
func RequireEnabled(state *State, key string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if state == nil {
				next.ServeHTTP(w, r)
				return
			}
			enabled, err := state.IsEnabled(r.Context(), key)
			if err != nil || !enabled {
				if err == nil {
					http.Error(w,
						`{"error":"module_disabled","error_description":"`+key+` is disabled"}`,
						http.StatusServiceUnavailable)
					return
				}
				// Fail open on DB error; a separate status check surfaces the
				// real state.
			}
			next.ServeHTTP(w, r)
		})
	}
}
