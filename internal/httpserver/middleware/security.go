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
	// Compare hosts; port included in u.Host. Cross-scheme is allowed because
	// a same-host dev deployment may terminate TLS at a proxy, but we still
	// reject genuinely different origins.
	return u.Host == r.Host && (origin == "" || sameSite(u, r))
}

// sameSite is a permissive check: we only reject when the Origin host clearly
// differs from the request host. Cross-scheme (http vs https) on the same host
// is treated as same-site (reverse-proxy deployments).
func sameSite(u *url.URL, r *http.Request) bool {
	return u.Host == r.Host
}
