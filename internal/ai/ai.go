// Package ai wires the AI agent module: LLM provider abstraction, agent
// runtime, conversation persistence, and the user-facing + admin HTTP
// surfaces.
//
// The Module is constructed in internal/server. It owns the conv.Service,
// agent.Runtime, and DefinitionRepo; the provider repository is shared
// with the admin handler so admin writes are visible to runtime reads
// immediately.
package ai

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/headercat/airbrew/internal/ai/agent"
	"github.com/headercat/airbrew/internal/ai/conv"
	"github.com/headercat/airbrew/internal/ai/handler"
	"github.com/headercat/airbrew/internal/ai/provider"
	"github.com/headercat/airbrew/internal/audit"
	"github.com/headercat/airbrew/internal/modules"
)

// Module bundles the AI agent services and HTTP handlers.
type Module struct {
	state   *modules.State
	prov    *provider.Repository
	conv    *conv.Service
	agents  *agent.DefinitionRepo
	runtime *agent.Runtime
	user    *handler.Handler
	admin   *handler.AdminHandler
}

// New constructs a Module from its dependencies.
//
//   - provRepo stores + resolves provider configs.
//   - convSvc persists conversations and messages.
//   - agentsRepo persists agent definitions.
//   - tools is the registry of tools the runtime can dispatch.
//   - resolveProvider returns the active LLMClient; nil leaves chat
//     disabled with a friendly 503.
//   - state drives the module-gating middleware.
//   - auditSvc records state-changing events.
func New(
	state *modules.State,
	provRepo *provider.Repository,
	convSvc *conv.Service,
	agentsRepo *agent.DefinitionRepo,
	tools *agent.ToolRegistry,
	resolveProvider func(ctx context.Context) (provider.LLMClient, error),
	auditSvc *audit.Service,
) *Module {
	var runtime *agent.Runtime
	var autoTitle func(ctx context.Context, userID, conversationID, userMessage string) (string, error)
	if resolveProvider != nil {
		runtime = agent.New(convSvc, tools, resolveProvider)
		autoTitle = runtime.AutoTitle
	}
	return &Module{
		state:   state,
		prov:    provRepo,
		conv:    convSvc,
		agents:  agentsRepo,
		runtime: runtime,
		user:    handler.New(convSvc, agentsRepo, runtime, auditSvc).WithAutoTitle(autoTitle),
		admin:   handler.NewAdmin(provRepo, agentsRepo, convSvc, tools, auditSvc),
	}
}

// Status is the public GET /api/ai/status handler.
func (m *Module) Status(w http.ResponseWriter, r *http.Request) {
	enabled := true
	if m.state != nil {
		if v, err := m.state.IsEnabled(r.Context(), "ai"); err == nil {
			enabled = v
		}
	}
	status := "ok"
	if !enabled {
		status = "disabled"
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"module":  "ai",
		"status":  status,
		"enabled": enabled,
		"drivers": provider.Drivers(),
	})
}

// RegisterPublicRoutes mounts the status endpoint (no auth).
func (m *Module) RegisterPublicRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/ai/status", m.Status)
}

// RegisterUserRoutes mounts the session-protected user endpoints. Caller
// is expected to wrap mux with the session middleware + module gate +
// rate limiter.
func (m *Module) RegisterUserRoutes(mux *http.ServeMux) {
	m.user.RegisterUserRoutes(mux)
}

// RegisterAdminRoutes mounts the admin endpoints. Caller mounts mux
// inside the RequireAdmin-wrapped admin tree.
func (m *Module) RegisterAdminRoutes(mux *http.ServeMux) {
	m.admin.RegisterRoutes(mux)
}

// Seed ensures the built-in agents exist; call once at startup.
func (m *Module) Seed(ctx context.Context) error {
	if m.agents == nil {
		return nil
	}
	defaultModel := ""
	if cli, err := m.prov.Resolve(context.Background(), provider.DirectionChat); err == nil {
		defaultModel = cli.Model()
	}
	return m.agents.Seed(ctx, agent.DefaultBuiltins(defaultModel))
}

// AutoTitle generates and stores a title for the named conversation. It
// is best-effort: errors are swallowed by the caller (the stream
// handler) so a title-gen failure never breaks chat.
func (m *Module) AutoTitle(ctx context.Context, userID, conversationID, userMessage string) (string, error) {
	if m.runtime == nil {
		return "", errors.New("ai: runtime not configured")
	}
	return m.runtime.AutoTitle(ctx, userID, conversationID, userMessage)
}

// HeartbeatInterval is the keep-alive period for the SSE stream. Exported
// so tests can shrink it.
const HeartbeatInterval = 15 * time.Second
