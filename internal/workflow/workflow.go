// Package workflow is reserved for the automation workflow module.
package workflow

import (
	"database/sql"
	"encoding/json"
	"net/http"

	"github.com/headercat/airbrew/internal/audit"
	"github.com/headercat/airbrew/internal/modules"
	wfexec "github.com/headercat/airbrew/internal/workflow/exec"
	"github.com/headercat/airbrew/internal/workflow/handler"
	"github.com/headercat/airbrew/internal/workflow/run"
)

type Module struct {
	state  *modules.State
	svc    *run.Service
	engine *wfexec.Engine
	h      *handler.Handler
}

func New(db *sql.DB, state *modules.State, auditSvc *audit.Service) *Module {
	repo := run.NewRepository(db)
	svc := run.NewService(repo, auditSvc)
	engine := wfexec.New(svc, nil)
	return &Module{state: state, svc: svc, engine: engine, h: handler.New(svc, engine)}
}

func (m *Module) RegisterRoutes(mux *http.ServeMux) {
	m.h.RegisterRoutes(mux)
}

func (m *Module) RegisterPublicRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/workflow/status", m.status)
	m.h.RegisterPublicRoutes(mux)
}

func (m *Module) status(w http.ResponseWriter, r *http.Request) {
	enabled := true
	if m.state != nil {
		if v, err := m.state.IsEnabled(r.Context(), "workflow"); err == nil {
			enabled = v
		}
	}
	status := "ok"
	if !enabled {
		status = "disabled"
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"module": "workflow", "status": status, "enabled": enabled,
	})
}
