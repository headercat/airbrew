// Package drive wires the drive module: a per-user file-and-folder tree backed
// by the blob store, with uploads, downloads, move/copy, star, trash, and
// public share links.
//
// Layout (see docs/drive.md):
//
//	internal/drive/files    Node + Share domain, repository, service, janitor
//	internal/drive/handler  JSON HTTP handlers (user + admin + public share)
//
// The module is registered in internal/modules.Catalog and mounted in
// internal/server under /api/drive. The status endpoint is public; the other
// user endpoints require a browser session (enforced by the session middleware
// in server.go); the share endpoints under /api/drive/s/{token} are public.
// Per-workspace storage limits are stored in server_settings under the key
// module.drive.config as JSON and applied live (no restart needed).
package drive

import (
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/headercat/airbrew/internal/audit"
	"github.com/headercat/airbrew/internal/blob"
	"github.com/headercat/airbrew/internal/drive/files"
	"github.com/headercat/airbrew/internal/drive/handler"
	"github.com/headercat/airbrew/internal/modules"
)

// Module bundles the drive services and HTTP handlers.
type Module struct {
	state *modules.State
	audit *audit.Service
	svc   *files.Service
	h     *handler.Handler
	admin *handler.AdminHandler
}

// New builds the drive Module bound to the given database. It loads the stored
// storage limits and starts the background janitor (orphan blob sweep + expired
// share deactivation) when ctx is non-nil.
func New(ctx context.Context, db *sql.DB, state *modules.State, auditSvc *audit.Service, blobs blob.Store) *Module {
	repo := files.NewRepository(db)
	svc := files.NewService(repo, blobs, repo.GetConfig(context.Background()))
	if ctx != nil {
		jan := files.NewJanitor(repo, blobs, slog.Default())
		go jan.Start(ctx)
	}
	return &Module{
		state: state,
		audit: auditSvc,
		svc:   svc,
		h:     handler.New(svc, blobs, auditSvc),
		admin: handler.NewAdmin(svc, repo, auditSvc),
	}
}

// Status is the public GET /api/drive/status handler.
func (m *Module) Status(w http.ResponseWriter, r *http.Request) {
	enabled := true
	if m.state != nil {
		if v, err := m.state.IsEnabled(r.Context(), "drive"); err == nil {
			enabled = v
		}
	}
	status := "ok"
	if !enabled {
		status = "disabled"
	}
	cfg := m.svc.Config()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"module":           "drive",
		"status":           status,
		"enabled":          enabled,
		"max_upload_bytes": cfg.MaxUploadBytes,
		"quota_bytes":      cfg.QuotaBytes,
	})
}

// RegisterPublicRoutes mounts routes reachable without a session: the status
// endpoint and the public share accessors.
func (m *Module) RegisterPublicRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/drive/status", m.Status)
	m.h.RegisterShareRoutes(mux)
}

// RegisterRoutes mounts the authenticated user endpoints on mux. The caller is
// expected to wrap mux with the session middleware.
func (m *Module) RegisterRoutes(mux *http.ServeMux) {
	m.h.RegisterRoutes(mux)
}

// RegisterAdminRoutes mounts the admin endpoints on mux. The caller is expected
// to mount mux within the admin (RequireAdmin) tree.
func (m *Module) RegisterAdminRoutes(mux *http.ServeMux) {
	m.admin.RegisterRoutes(mux)
}
