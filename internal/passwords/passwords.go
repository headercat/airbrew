// Package passwords wires the zero-knowledge password-vault module.
//
// The module is registered in internal/modules.Catalog and mounted in
// internal/server under /api/vault. The status endpoint follows the same
// convention as the other feature modules; all other endpoints require an
// authenticated browser session (enforced by the session middleware in
// server.go) and carry only ciphertext.
//
// On construction (when ctx is non-nil) the module starts a background janitor
// that purges old tombstones and orphaned attachment blobs; the loop exits when
// ctx is cancelled.
package passwords

import (
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/headercat/airbrew/internal/audit"
	"github.com/headercat/airbrew/internal/blob"
	"github.com/headercat/airbrew/internal/modules"
	"github.com/headercat/airbrew/internal/logging"
	"github.com/headercat/airbrew/internal/passwords/handler"
	"github.com/headercat/airbrew/internal/passwords/vault"
)

// Module bundles the vault service and HTTP handler.
type Module struct {
	svc   *vault.Service
	state *modules.State
	audit *audit.Service
	h     *handler.Handler
}

// New builds the passwords Module. blobs backs attachment upload/download.
func New(ctx context.Context, db *sql.DB, state *modules.State, auditSvc *audit.Service, blobs blob.Store) *Module {
	repo := vault.NewRepository(db)
	svc := vault.NewService(repo)
	// Start the background janitor (tombstone purge + orphan blob sweep). It
	// self-cancels with ctx; on a nil ctx we skip it (e.g. in tests).
	if ctx != nil {
		jan := vault.NewJanitor(repo, blobs, slog.Default())
		logging.Go("passwords.janitor", func() { jan.Start(ctx) })
	}
	return &Module{
		svc:   svc,
		state: state,
		audit: auditSvc,
		h:     handler.New(svc, auditSvc, blobs),
	}
}

// RegisterRoutes mounts the authenticated vault endpoints on mux. The caller
// is expected to wrap mux with the session middleware.
func (m *Module) RegisterRoutes(mux *http.ServeMux) {
	m.h.RegisterRoutes(mux)
}

// RegisterKeyRoutes mounts only the envelope setup/rotate endpoints so the
// server can apply a stricter rate limit to them.
func (m *Module) RegisterKeyRoutes(mux *http.ServeMux) {
	m.h.RegisterKeyRoutes(mux)
}

// Status is the public GET /api/vault/status handler (no auth), mirroring the
// other feature modules so the SPA can list and gate the module.
func (m *Module) Status(w http.ResponseWriter, r *http.Request) {
	enabled := true
	if m.state != nil {
		if v, err := m.state.IsEnabled(r.Context(), "passwords"); err == nil {
			enabled = v
		}
	}
	status := "ok"
	if !enabled {
		status = "disabled"
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{
		"module":  "passwords",
		"status":  status,
		"enabled": boolStr(enabled),
	})
}

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}
