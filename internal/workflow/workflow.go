// Package workflow is reserved for the automation workflow module.
package workflow

import (
	"encoding/json"
	"net/http"

	"github.com/headercat/airbrew/internal/modules"
)

type Module struct {
	state *modules.State
}

func New(state *modules.State) *Module { return &Module{state: state} }

func (m *Module) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/workflow/status", m.status)
}

func (m *Module) status(w http.ResponseWriter, r *http.Request) {
	enabled := true
	if m.state != nil {
		if v, err := m.state.IsEnabled(r.Context(), "workflow"); err == nil {
			enabled = v
		}
	}
	status := "not_implemented"
	if !enabled {
		status = "disabled"
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{
		"module": "workflow", "status": status, "enabled": boolStr(enabled),
	})
}

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}
