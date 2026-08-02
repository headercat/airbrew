// Package handler — admin.go
//
// Admin endpoints for provider configuration and agent management. All
// routes are mounted under the admin tree (session + RequireAdmin).
package handler

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/headercat/airbrew/internal/ai/agent"
	"github.com/headercat/airbrew/internal/ai/provider"
	"github.com/headercat/airbrew/internal/audit"
	"github.com/headercat/airbrew/internal/httpserver/response"
)

// AdminHandler exposes admin-only AI endpoints.
type AdminHandler struct {
	prov   *provider.Repository
	agents *agent.DefinitionRepo
	audit  *audit.Service
}

// NewAdmin builds an AdminHandler.
func NewAdmin(prov *provider.Repository, agents *agent.DefinitionRepo, auditSvc *audit.Service) *AdminHandler {
	return &AdminHandler{prov: prov, agents: agents, audit: auditSvc}
}

// RegisterRoutes mounts the admin endpoints. The caller mounts this mux
// inside the RequireAdmin-wrapped admin tree.
func (a *AdminHandler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/admin/ai/providers", a.listProviders)
	mux.HandleFunc("PUT /api/admin/ai/providers/{direction}", a.putProvider)
	mux.HandleFunc("DELETE /api/admin/ai/providers/{id}", a.deleteProvider)

	mux.HandleFunc("GET /api/admin/ai/agents", a.listAgents)
	mux.HandleFunc("POST /api/admin/ai/agents", a.createAgent)
	mux.HandleFunc("PUT /api/admin/ai/agents/{id}", a.updateAgent)
	mux.HandleFunc("DELETE /api/admin/ai/agents/{id}", a.deleteAgent)

	mux.HandleFunc("GET /api/admin/ai/drivers", a.listDrivers)
}

// --- providers ------------------------------------------------------------

type providerResp struct {
	ID         string    `json:"id"`
	Direction  string    `json:"direction"`
	Driver     string    `json:"driver"`
	Name       string    `json:"name"`
	BaseURL    string    `json:"base_url"`
	ModelHint  string    `json:"model_hint"`
	IsActive   bool      `json:"is_active"`
	HasAPIKey  bool      `json:"has_api_key"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

func toProviderResp(p provider.StoredProvider) providerResp {
	return providerResp{
		ID: p.ID, Direction: string(p.Direction), Driver: p.Driver,
		Name: p.Name, BaseURL: p.BaseURL, ModelHint: p.ModelHint,
		IsActive: p.IsActive, HasAPIKey: p.HasAPIKey,
		CreatedAt: p.CreatedAt, UpdatedAt: p.UpdatedAt,
	}
}

func (a *AdminHandler) listProviders(w http.ResponseWriter, r *http.Request) {
	provs, err := a.prov.List(r.Context())
	if err != nil {
		sanitizeInternal(w, err)
		return
	}
	out := make([]providerResp, 0, len(provs))
	for _, p := range provs {
		out = append(out, toProviderResp(provider.SafeForJSON(p)))
	}
	response.JSON(w, http.StatusOK, map[string]any{"providers": out})
}

type putProviderReq struct {
	Driver    string `json:"driver"`
	Name      string `json:"name"`
	BaseURL   string `json:"base_url"`
	ModelHint string `json:"model_hint"`
	APIKey    string `json:"api_key"`
}

func (a *AdminHandler) putProvider(w http.ResponseWriter, r *http.Request) {
	sess, ok := requireSession(w, r)
	if !ok {
		return
	}
	direction := provider.Direction(r.PathValue("direction"))
	var req putProviderReq
	if err := decodeJSON(r, &req); err != nil {
		response.Error(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	p, err := a.prov.Put(r.Context(), direction, req.Driver, req.Name,
		strings.TrimSpace(req.BaseURL), strings.TrimSpace(req.ModelHint), req.APIKey)
	if err != nil {
		writeProviderError(w, err)
		return
	}
	a.audit.Log(r.Context(), audit.Entry{
		EventType: "ai.provider_upserted", ActorUserID: sess.UserID,
		TargetType: "ai_provider", TargetID: p.ID,
		IPAddress: clientIP(r), UserAgent: r.UserAgent(),
		Metadata: map[string]any{
			"direction": string(direction), "driver": req.Driver,
			"has_api_key": req.APIKey != "",
		},
	})
	response.JSON(w, http.StatusOK, toProviderResp(provider.SafeForJSON(p)))
}

func (a *AdminHandler) deleteProvider(w http.ResponseWriter, r *http.Request) {
	sess, ok := requireSession(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	if err := a.prov.Delete(r.Context(), id); err != nil {
		writeProviderError(w, err)
		return
	}
	a.audit.Log(r.Context(), audit.Entry{
		EventType: "ai.provider_deleted", ActorUserID: sess.UserID,
		TargetType: "ai_provider", TargetID: id,
		IPAddress: clientIP(r), UserAgent: r.UserAgent(),
	})
	response.JSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// --- agents ---------------------------------------------------------------

type adminAgentResp struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	Description  string   `json:"description"`
	Model        string   `json:"model"`
	SystemPrompt string   `json:"system_prompt"`
	Tools        []string `json:"tools"`
	Temperature  float32  `json:"temperature"`
	MaxTokens    int      `json:"max_tokens"`
	MaxTurns     int      `json:"max_turns"`
	IsBuiltin    bool     `json:"is_builtin"`
	IsActive     bool     `json:"is_active"`
}

func toAdminAgentResp(a agent.Definition) adminAgentResp {
	tools := a.Tools
	if tools == nil {
		tools = []string{}
	}
	return adminAgentResp{
		ID: a.ID, Name: a.Name, Description: a.Description, Model: a.Model,
		SystemPrompt: a.SystemPrompt, Tools: tools,
		Temperature: a.Temperature, MaxTokens: a.MaxTokens, MaxTurns: a.MaxTurns,
		IsBuiltin: a.IsBuiltin, IsActive: a.IsActive,
	}
}

func (a *AdminHandler) listAgents(w http.ResponseWriter, r *http.Request) {
	agents, err := a.agents.List(r.Context(), false)
	if err != nil {
		sanitizeInternal(w, err)
		return
	}
	out := make([]adminAgentResp, 0, len(agents))
	for _, ag := range agents {
		out = append(out, toAdminAgentResp(ag))
	}
	response.JSON(w, http.StatusOK, map[string]any{"agents": out})
}

type agentReq struct {
	Name         string   `json:"name"`
	Description  string   `json:"description"`
	Model        string   `json:"model"`
	SystemPrompt string   `json:"system_prompt"`
	Tools        []string `json:"tools"`
	Temperature  float32  `json:"temperature"`
	MaxTokens    int      `json:"max_tokens"`
	MaxTurns     int      `json:"max_turns"`
	IsActive     bool     `json:"is_active"`
}

func (a *AdminHandler) createAgent(w http.ResponseWriter, r *http.Request) {
	sess, ok := requireSession(w, r)
	if !ok {
		return
	}
	var req agentReq
	if err := decodeJSON(r, &req); err != nil {
		response.Error(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	created, err := a.agents.Create(r.Context(), agent.Input{
		Name: req.Name, Description: req.Description, Model: req.Model,
		SystemPrompt: req.SystemPrompt, Tools: req.Tools,
		Temperature: req.Temperature, MaxTokens: req.MaxTokens,
		MaxTurns: req.MaxTurns, IsActive: req.IsActive,
	})
	if err != nil {
		writeAgentError(w, err)
		return
	}
	a.audit.Log(r.Context(), audit.Entry{
		EventType: "ai.agent_created", ActorUserID: sess.UserID,
		TargetType: "ai_agent", TargetID: created.ID,
		IPAddress: clientIP(r), UserAgent: r.UserAgent(),
		Metadata: map[string]any{"name": created.Name},
	})
	response.JSON(w, http.StatusCreated, toAdminAgentResp(created))
}

func (a *AdminHandler) updateAgent(w http.ResponseWriter, r *http.Request) {
	sess, ok := requireSession(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	var req agentReq
	if err := decodeJSON(r, &req); err != nil {
		response.Error(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	updated, err := a.agents.Update(r.Context(), id, agent.Input{
		Name: req.Name, Description: req.Description, Model: req.Model,
		SystemPrompt: req.SystemPrompt, Tools: req.Tools,
		Temperature: req.Temperature, MaxTokens: req.MaxTokens,
		MaxTurns: req.MaxTurns, IsActive: req.IsActive,
	})
	if err != nil {
		writeAgentError(w, err)
		return
	}
	a.audit.Log(r.Context(), audit.Entry{
		EventType: "ai.agent_updated", ActorUserID: sess.UserID,
		TargetType: "ai_agent", TargetID: id,
		IPAddress: clientIP(r), UserAgent: r.UserAgent(),
		Metadata: map[string]any{"name": updated.Name},
	})
	response.JSON(w, http.StatusOK, toAdminAgentResp(updated))
}

func (a *AdminHandler) deleteAgent(w http.ResponseWriter, r *http.Request) {
	sess, ok := requireSession(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	if err := a.agents.Delete(r.Context(), id); err != nil {
		writeAgentError(w, err)
		return
	}
	a.audit.Log(r.Context(), audit.Entry{
		EventType: "ai.agent_deleted", ActorUserID: sess.UserID,
		TargetType: "ai_agent", TargetID: id,
		IPAddress: clientIP(r), UserAgent: r.UserAgent(),
	})
	response.JSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// --- drivers --------------------------------------------------------------

type driverInfo struct {
	Name         string `json:"name"`
	NeedsAPIKey  bool   `json:"needs_api_key"`
	DefaultModel string `json:"default_model"`
}

func (a *AdminHandler) listDrivers(w http.ResponseWriter, r *http.Request) {
	names := provider.Drivers()
	out := make([]driverInfo, 0, len(names))
	for _, n := range names {
		d, _ := provider.LookupDriver(n)
		out = append(out, driverInfo{
			Name: n, NeedsAPIKey: d.NeedsAPIKey(),
			DefaultModel: d.DefaultModel(),
		})
	}
	response.JSON(w, http.StatusOK, map[string]any{"drivers": out})
}

// --- error helpers --------------------------------------------------------

func writeProviderError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, provider.ErrNotFound):
		response.Error(w, http.StatusNotFound, "not_found", "provider not found")
	case errors.Is(err, provider.ErrInvalidInput), errors.Is(err, provider.ErrInvalidConfig):
		response.Error(w, http.StatusBadRequest, "invalid_request", err.Error())
	case errors.Is(err, provider.ErrUnsupportedDriver):
		response.Error(w, http.StatusBadRequest, "unknown_driver", err.Error())
	default:
		sanitizeInternal(w, err)
	}
}

func writeAgentError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, agent.ErrNotFound):
		response.Error(w, http.StatusNotFound, "not_found", "agent not found")
	case errors.Is(err, agent.ErrInvalidInput):
		response.Error(w, http.StatusBadRequest, "invalid_request", err.Error())
	default:
		sanitizeInternal(w, err)
	}
}
