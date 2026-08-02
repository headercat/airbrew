// Package drive wires the drive module: a per-user file-and-folder tree backed
// by the blob store, with uploads, downloads, move/copy, star, trash, and
// public share links.
//
// Layout (see docs/drive.md):
//
//	internal/drive/files    Node + Share domain, repository, service
//	internal/drive/handler  JSON HTTP handlers (user + public share)
//
// The module is registered in internal/modules.Catalog and mounted in
// internal/server under /api/drive. The status endpoint is public; the other
// user endpoints require a browser session (enforced by the session middleware
// in server.go); the share endpoints under /api/drive/s/{token} are public.
package drive

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"

	"github.com/headercat/airbrew/internal/audit"
	"github.com/headercat/airbrew/internal/blob"
	"github.com/headercat/airbrew/internal/drive/files"
	"github.com/headercat/airbrew/internal/drive/handler"
	"github.com/headercat/airbrew/internal/modules"
)

const (
	// defaultMaxUpload is the default per-file upload cap (50 MiB).
	defaultMaxUpload int64 = 50 << 20
	// defaultQuota is the default per-user storage cap (1 GiB).
	defaultQuota int64 = 1 << 30
)

// Module bundles the drive services and HTTP handlers.
type Module struct {
	state *modules.State
	audit *audit.Service
	svc   *files.Service
	h     *handler.Handler
}

// New builds the drive Module bound to the given database.
func New(ctx context.Context, db *sql.DB, state *modules.State, auditSvc *audit.Service, blobs blob.Store) *Module {
	repo := files.NewRepository(db)
	svc := files.NewService(repo, blobs, files.Config{
		MaxUploadBytes: defaultMaxUpload,
		QuotaBytes:     defaultQuota,
	})
	return &Module{
		state: state,
		audit: auditSvc,
		svc:   svc,
		h:     handler.New(svc, blobs, auditSvc),
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
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"module":           "drive",
		"status":           status,
		"enabled":          enabled,
		"max_upload_bytes": defaultMaxUpload,
		"quota_bytes":      defaultQuota,
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
