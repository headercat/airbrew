// Package server bundles root mux construction so cmd/airbrew stays thin.
package server

import (
	"context"
	"io"
	"io/fs"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"

	"github.com/headercat/airbrew/internal/admin"
	"github.com/headercat/airbrew/internal/ai"
	"github.com/headercat/airbrew/internal/auth"
	"github.com/headercat/airbrew/internal/blob"
	"github.com/headercat/airbrew/internal/chat"
	"github.com/headercat/airbrew/internal/contacts"
	"github.com/headercat/airbrew/internal/db"
	"github.com/headercat/airbrew/internal/drive"
	"github.com/headercat/airbrew/internal/httpserver/middleware"
	"github.com/headercat/airbrew/internal/mail"
	"github.com/headercat/airbrew/internal/modules"
	"github.com/headercat/airbrew/internal/passwords"
	"github.com/headercat/airbrew/internal/workflow"
)

// Deps bundles everything the mux needs.
type Deps struct {
	DB             *db.DB
	SessionMax     time.Duration
	WebFS          fs.FS
	WebProxyTarget string
	Blobs          blob.Store
	// Ctx is the process lifecycle context. Modules use it to start background
	// loops (e.g. the vault janitor) that should stop on shutdown.
	Ctx context.Context
	// CookieSecure marks the session cookie Secure (HTTPS-only). Required for
	// production; false for local HTTP dev.
	CookieSecure bool
}

// Build returns the root *http.ServeMux wired with every module.
func Build(d Deps) *http.ServeMux {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	})

	if d.Blobs != nil {
		mux.Handle("/api/files/", filesHandler(d.Blobs))
	}

	// Admin module.
	adminMod := admin.New(d.DB.DB)
	adminMod.RegisterPublicRoutes(mux) // public: GET /api/branding, GET /api/admin/status

	authMod := auth.New(d.DB.DB, auth.Config{SessionMaxAge: d.SessionMax, Blobs: d.Blobs, Audit: adminMod.Audit(), CookieSecure: d.CookieSecure})
	authSub := http.NewServeMux()
	authMod.Handler.RegisterRoutes(authSub)
	mux.Handle("/api/auth/", authMod.SessionMiddleware(authSub))

	adminSub := http.NewServeMux()
	adminMod.RegisterRoutes(adminSub)
	mux.Handle("/api/admin/", authMod.SessionMiddleware(adminSub))

	// Other feature modules. Each registers /api/<name>/status.
	stubState := adminMod.State()

	// Mail module. Status + inbound webhooks are public; mailbox/message/send
	// endpoints require a session; provider config lives under the admin tree.
	mailMod := mail.New(d.DB, stubState, adminMod.Audit(), d.Blobs)
	mailMod.RegisterPublicRoutes(mux) // GET /api/mail/status, POST /api/mail/inbound/{driver}
	mailSub := http.NewServeMux()
	mailMod.RegisterRoutes(mailSub)
	mux.Handle("/api/mail/", authMod.SessionMiddleware(mailSub))
	// Admin provider config: mount under a RequireAdmin-wrapped mux alongside
	// the rest of the admin tree (which is itself SessionMiddleware-wrapped).
	mailAdminSub := http.NewServeMux()
	mailMod.RegisterAdminRoutes(mailAdminSub)
	adminSub.Handle("/api/admin/mail/", admin.RequireAdmin(authMod.UserRepo)(mailAdminSub))
	mailMod.Start(d.Ctx) // inbound poll coordinator

	// Drive module. Status + public share links are public; file/folder/share
	// management endpoints require a session; storage limits live under admin.
	driveMod := drive.New(d.Ctx, d.DB.DB, stubState, adminMod.Audit(), d.Blobs)
	driveMod.RegisterPublicRoutes(mux) // GET /api/drive/status, GET /api/drive/s/{token}
	driveSub := http.NewServeMux()
	driveMod.RegisterRoutes(driveSub)
	mux.Handle("/api/drive/", authMod.SessionMiddleware(driveSub))
	driveAdminSub := http.NewServeMux()
	driveMod.RegisterAdminRoutes(driveAdminSub)
	adminSub.Handle("/api/admin/drive/", admin.RequireAdmin(authMod.UserRepo)(driveAdminSub))

	contacts.New(stubState).RegisterRoutes(mux)
	chat.New(stubState).RegisterRoutes(mux)
	ai.New(stubState).RegisterRoutes(mux)
	workflow.New(stubState).RegisterRoutes(mux)

	// Password vault. Status is public; the remaining endpoints require a
	// session, so they are mounted on a sub-mux wrapped in SessionMiddleware.
	// The sub-mux additionally enforces module-disable gating (so an admin can
	// actually take the vault offline) and a per-IP rate limit.
	pwMod := passwords.New(d.Ctx, d.DB.DB, stubState, adminMod.Audit(), d.Blobs)
	mux.HandleFunc("GET /api/vault/status", pwMod.Status)
	pwSub := http.NewServeMux()
	pwMod.RegisterRoutes(pwSub)
	vaultLimiter := middleware.NewRateLimiter(120, time.Minute)
	mux.Handle("/api/vault/", authMod.SessionMiddleware(middleware.Chain(pwSub,
		modules.RequireEnabled(stubState, "passwords"),
		middleware.RateLimit(vaultLimiter, middleware.ClientIPKey),
	)))

	if d.WebProxyTarget != "" {
		mux.Handle("/", webProxyHandler(d.WebProxyTarget))
	} else {
		mux.Handle("/", spaHandler(d.WebFS))
	}

	return mux
}

// filesHandler serves files from a blob.Store at /api/files/<namespace>/<name>.
func filesHandler(store blob.Store) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rel := strings.TrimPrefix(r.URL.Path, "/api/files/")
		if rel == "" || strings.Contains(rel, "..") {
			http.NotFound(w, r)
			return
		}
		body, ct, err := store.Open(r.Context(), rel)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		defer body.Close()
		w.Header().Set("Content-Type", ct)
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		if _, err := io.Copy(w, body); err != nil {
			return
		}
	})
}

// spaHandler serves embedded files, returning index.html for unknown paths so
// client-side routing works (history API).
func spaHandler(webFS fs.FS) http.Handler {
	fileServer := http.FileServerFS(webFS)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		if path == "/" {
			fileServer.ServeHTTP(w, r)
			return
		}
		if _, err := fs.Stat(webFS, path[1:]); err != nil {
			r2 := r.Clone(r.Context())
			r2.URL.Path = "/"
			fileServer.ServeHTTP(w, r2)
			return
		}
		fileServer.ServeHTTP(w, r)
	})
}

func webProxyHandler(target string) http.Handler {
	u, err := url.Parse(target)
	if err != nil {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "invalid AIRBREW_WEB_PROXY_TARGET", http.StatusInternalServerError)
		})
	}
	return httputil.NewSingleHostReverseProxy(u)
}
