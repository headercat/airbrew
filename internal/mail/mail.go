// Package mail is reserved for the mail module.
//
// Planned surface (post-auth milestones):
//   - IMAP/SMTP-style mailboxes
//   - inbound + outbound message storage
//   - per-thread search and tags
//   - integration with the contacts module for sender enrichment
//
// The HTTP route GET /api/mail/status is exposed so the SPA can confirm
// the module is present and reports its enabled state from server_settings.
package mail

import (
	"encoding/json"
	"net/http"

	"github.com/headercat/airbrew/internal/modules"
)

// Module bundles the future mail functionality.
type Module struct {
	state *modules.State
}

// New returns a Module bound to the shared module-state service.
func New(state *modules.State) *Module { return &Module{state: state} }

// RegisterRoutes mounts stub routes on mux.
func (m *Module) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/mail/status", m.status)
}

func (m *Module) status(w http.ResponseWriter, r *http.Request) {
	enabled := true
	if m.state != nil {
		if v, err := m.state.IsEnabled(r.Context(), "mail"); err == nil {
			enabled = v
		}
	}
	status := "not_implemented"
	if !enabled {
		status = "disabled"
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{
		"module":  "mail",
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
