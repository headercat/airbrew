// Package contacts wires the contacts (address book) module: per-user contacts
// with structured names, multi-value emails/phones/addresses/ims/urls, notes,
// optional avatars, user-defined groups (labels) with many-to-many membership,
// and vCard 4.0 import/export.
//
// Layout (see docs/contacts.md):
//
//	internal/contacts/contact   Contact + Group domain, repository, service, vCard
//	internal/contacts/handler   JSON HTTP handlers
//
// The module is registered in internal/modules.Catalog and mounted in
// internal/server under /api/contacts. The status endpoint is public; the other
// user endpoints require a browser session (enforced by the session middleware
// in server.go) and module-enable gating. Avatars live in the blob store under
// namespace "contacts".
package contacts

import (
	"database/sql"
	"encoding/json"
	"net/http"

	"github.com/headercat/airbrew/internal/audit"
	"github.com/headercat/airbrew/internal/blob"
	"github.com/headercat/airbrew/internal/contacts/contact"
	"github.com/headercat/airbrew/internal/contacts/handler"
	"github.com/headercat/airbrew/internal/modules"
)

// Module bundles the contacts services and HTTP handlers.
type Module struct {
	state *modules.State
	audit *audit.Service
	svc   *contact.Service
	h     *handler.Handler
}

// New builds the contacts Module bound to the given database. blobs stores
// avatar bytes (may be nil to disable avatar uploads).
func New(db *sql.DB, state *modules.State, auditSvc *audit.Service, blobs blob.Store) *Module {
	repo := contact.NewRepository(db)
	svc := contact.NewService(repo, blobs)
	return &Module{
		state: state,
		audit: auditSvc,
		svc:   svc,
		h:     handler.New(svc, blobs, auditSvc),
	}
}

// Status is the public GET /api/contacts/status handler.
func (m *Module) Status(w http.ResponseWriter, r *http.Request) {
	enabled := true
	if m.state != nil {
		if v, err := m.state.IsEnabled(r.Context(), "contacts"); err == nil {
			enabled = v
		}
	}
	status := "ok"
	if !enabled {
		status = "disabled"
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"module":  "contacts",
		"status":  status,
		"enabled": enabled,
	})
}

// RegisterPublicRoutes mounts routes reachable without a session (the status
// endpoint).
func (m *Module) RegisterPublicRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/contacts/status", m.Status)
}

// RegisterRoutes mounts the authenticated user endpoints on mux. The caller is
// expected to wrap mux with the session middleware.
func (m *Module) RegisterRoutes(mux *http.ServeMux) {
	m.h.RegisterRoutes(mux)
}
