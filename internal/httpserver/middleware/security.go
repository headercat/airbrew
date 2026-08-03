// Package middleware — security.go
//
// Security middleware shared across the API and SPA: defensive response
// headers (including a production Content-Security-Policy that permits the
// vault's argon2id WASM), and a same-origin CSRF check for cookie-authenticated
// state-changing requests.
package middleware

import (
	"net/http"
	"net/url"
	"strings"
)

// SecurityHeaders sets a baseline of defensive response headers on every
// response. When strict is true (production SPA mode), a Content-Security-
// Policy is also applied that allows only same-origin resources plus the
// 'wasm-unsafe-eval' needed by the vault's argon2id WASM build; in dev the
// Vite dev server is proxied and HMR needs looser rules, so CSP is skipped.
//
// HSTS is only emitted when the inbound request is TLS so local HTTP dev is
// not pinned to https.
func SecurityHeaders(strict bool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h := w.Header()
			h.Set("X-Content-Type-Options", "nosniff")
			h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
			h.Set("X-Frame-Options", "SAMEORIGIN")
			h.Set("Permissions-Policy", "geolocation=(), microphone=(), camera=()")
			if r.TLS != nil {
				h.Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
			}
			if strict {
				// 'wasm-unsafe-eval' is required for hash-wasm (argon2id).
				// style-src 'unsafe-inline' because shadcn/radix apply inline
				// styles; this does not weaken script protection.
				h.Set("Content-Security-Policy", strings.Join([]string{
					"default-src 'self'",
					"script-src 'self' 'wasm-unsafe-eval'",
					"style-src 'self' 'unsafe-inline'",
					"img-src 'self' data: blob:",
					"font-src 'self' data:",
					"connect-src 'self'",
					"object-src 'none'",
					"base-uri 'self'",
					"frame-ancestors 'self'",
					"form-action 'self'",
				}, "; "))
			}
			next.ServeHTTP(w, r)
		})
	}
}

// CSRF guards cookie-authenticated state-changing requests by requiring their
// Origin (or Referer fallback) to be same-origin with the target. Browsers
// always send Origin on cross-site POST/PUT/PATCH/DELETE fetches; a missing
// Origin on a same-origin request is allowed (some user agents omit it).
// Safe methods (GET/HEAD/OPTIONS) and the health check are exempt.
//
// This complements the SameSite=Lax session cookie and is the OWASP-recommended
// mitigation for the class of CSRF that SameSite alone does not cover.
func CSRF(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !isStateChanging(r.Method) || r.URL.Path == "/healthz" {
			next.ServeHTTP(w, r)
			return
		}
		if allowedOrigin(r) {
			next.ServeHTTP(w, r)
			return
		}
		http.Error(w, `{"error":"forbidden","error_description":"cross-origin not allowed"}`,
			http.StatusForbidden)
	})
}

func isStateChanging(method string) bool {
	switch method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	}
	return false
}

// allowedOrigin reports whether the request's Origin (or Referer fallback) is
// same-origin/same-site with the server's Host. Same-origin browser requests
// legitimately omit Origin, which we accept.
func allowedOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	src := origin
	if src == "" {
		src = r.Header.Get("Referer")
	}
	if src == "" {
		return true // same-origin requests may omit both
	}
	u, err := url.Parse(src)
	if err != nil {
		return false
	}
	// Compare hosts; port included in u.Host. Cross-scheme on the same host is
	// allowed for reverse-proxy deployments that terminate TLS upstream —
	// unless the inbound request itself arrived over TLS (or via a proxy that
	// set X-Forwarded-Proto: https), in which case an http Origin is rejected
	// so a cleartext side-listener cannot forge requests.
	return u.Host == r.Host && (origin == "" || sameSite(u, r))
}

// requestIsHTTPS reports whether the inbound request was transported over TLS
// (directly or via a trusted reverse proxy).
func requestIsHTTPS(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	// X-Forwarded-Proto is set by the fronting proxy; DirectClientIP/allowIP
	// gating already limits who can reach this code path with a spoofed header
	// to trusted proxy hops.
	return strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

// sameSite is a permissive check: we only reject when the Origin host clearly
// differs from the request host. When the request itself is HTTPS, an http
// Origin is rejected so a cleartext side-listener on the same host cannot
// satisfy the same-site check.
func sameSite(u *url.URL, r *http.Request) bool {
	if u.Host != r.Host {
		return false
	}
	if requestIsHTTPS(r) && u.Scheme == "http" {
		return false
	}
	return true
}
