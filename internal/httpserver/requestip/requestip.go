// Package requestip contains conservative request IP helpers.
package requestip

import (
	"net"
	"net/http"
)

// DirectClientIP returns the direct peer address without trusting forwarded
// headers. Security-sensitive paths that support trusted proxies should use
// security.ResolveRequestIP instead.
func DirectClientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil {
		return host
	}
	return r.RemoteAddr
}
