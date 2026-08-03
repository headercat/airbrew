// Package workflow is reserved for the automation workflow module.
package workflow

import (
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/headercat/airbrew/internal/audit"
	"github.com/headercat/airbrew/internal/logging"
	"github.com/headercat/airbrew/internal/modules"
	wfexec "github.com/headercat/airbrew/internal/workflow/exec"
	"github.com/headercat/airbrew/internal/workflow/handler"
	"github.com/headercat/airbrew/internal/workflow/run"
	"github.com/headercat/airbrew/internal/workflow/trigger"
)

type Module struct {
	state     *modules.State
	svc       *run.Service
	engine    *wfexec.Engine
	h         *handler.Handler
	scheduler *trigger.Scheduler
}

func New(db *sql.DB, state *modules.State, auditSvc *audit.Service, mailer wfexec.MailSender) *Module {
	repo := run.NewRepository(db)
	svc := run.NewService(repo, auditSvc)
	engine := wfexec.New(svc, mailer)
	return &Module{
		state: state, svc: svc, engine: engine,
		h:         handler.New(svc, engine),
		scheduler: trigger.NewScheduler(repo, engine, slog.Default()),
	}
}

func (m *Module) RegisterRoutes(mux *http.ServeMux) {
	m.h.RegisterRoutes(mux)
}

func (m *Module) RegisterPublicRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/workflow/status", m.status)
	m.h.RegisterPublicRoutes(mux)
}

// RegisterWebhookRoute mounts the public webhook trigger endpoint. The caller
// is expected to wrap it with a per-IP rate limiter (webhooks are otherwise
// unauthenticated and a leaked token could be fired as fast as a client can
// POST).
func (m *Module) RegisterWebhookRoute(mux *http.ServeMux) {
	m.h.RegisterWebhookRoute(mux)
}

// Start launches background trigger dispatchers.
func (m *Module) Start(ctx context.Context) {
	if ctx == nil {
		slog.WarnContext(context.Background(),
			"workflow: lifecycle context is nil; scheduler will not run")
		return
	}
	// Any run still marked "running" belongs to a previous process that died
	// mid-graph; this process cannot resume it, so surface it as failed.
	if n, err := m.svc.ReapStaleRunning(ctx); err != nil {
		slog.WarnContext(ctx, "workflow: reap stale running runs", "error", err)
	} else if n > 0 {
		slog.InfoContext(ctx, "workflow: marked interrupted runs as failed", "count", n)
	}
	logging.Go("workflow.scheduler", func() { m.scheduler.Start(ctx) })
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
