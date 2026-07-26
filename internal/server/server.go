// Package server bundles root mux construction so cmd/airbrew stays thin.
package server

import (
	"io"
	"io/fs"
	"net/http"
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
	"github.com/headercat/airbrew/internal/mail"
	"github.com/headercat/airbrew/internal/passwords"
	"github.com/headercat/airbrew/internal/workflow"
)

// Deps bundles everything the mux needs.
type Deps struct {
	DB         *db.DB
	SessionMax time.Duration
	WebFS      fs.FS
	Blobs      blob.Store
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

	authMod := auth.New(d.DB.DB, auth.Config{SessionMaxAge: d.SessionMax, Blobs: d.Blobs, Audit: adminMod.Audit()})
	authSub := http.NewServeMux()
	authMod.Handler.RegisterRoutes(authSub)
	mux.Handle("/api/auth/", authMod.SessionMiddleware(authSub))

	adminSub := http.NewServeMux()
	adminMod.RegisterRoutes(adminSub)
	mux.Handle("/api/admin/", authMod.SessionMiddleware(adminSub))

	// Other feature modules. Each registers /api/<name>/status.
	stubState := adminMod.State()
	mail.New(stubState).RegisterRoutes(mux)
	drive.New(stubState).RegisterRoutes(mux)
	contacts.New(stubState).RegisterRoutes(mux)
	chat.New(stubState).RegisterRoutes(mux)
	ai.New(stubState).RegisterRoutes(mux)
	workflow.New(stubState).RegisterRoutes(mux)

	// Password vault. Status is public; the remaining endpoints require a
	// session, so they are mounted on a sub-mux wrapped in SessionMiddleware.
	pwMod := passwords.New(d.DB.DB, stubState, adminMod.Audit(), d.Blobs)
	mux.HandleFunc("GET /api/vault/status", pwMod.Status)
	pwSub := http.NewServeMux()
	pwMod.RegisterRoutes(pwSub)
	mux.Handle("/api/vault/", authMod.SessionMiddleware(pwSub))

	mux.Handle("/", spaHandler(d.WebFS))

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
