// Package handler exposes the HTTP surface for the AI agent module.
//
// User routes (session required, module-gated):
//
//	GET    /api/ai/status
//	GET    /api/ai/agents
//	GET    /api/ai/conversations
//	POST   /api/ai/conversations
//	GET    /api/ai/conversations/{id}
//	DELETE /api/ai/conversations/{id}
//	POST   /api/ai/conversations/{id}/stream   (SSE)
//
// Admin routes (session + RequireAdmin):
//
//	See admin.go.
package handler

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/headercat/airbrew/internal/ai/agent"
	"github.com/headercat/airbrew/internal/ai/conv"
	"github.com/headercat/airbrew/internal/ai/provider"
	"github.com/headercat/airbrew/internal/audit"
	"github.com/headercat/airbrew/internal/auth/session"
	"github.com/headercat/airbrew/internal/httpserver/response"
)

// Handler exposes the user-facing AI endpoints.
type Handler struct {
	conv     *conv.Service
	agents   *agent.DefinitionRepo
	runtime  *agent.Runtime
	audit    *audit.Service
}

// New builds a Handler. runtime may be nil when providers are not
// configured; in that case chat returns a friendly 503.
func New(c *conv.Service, a *agent.DefinitionRepo, rt *agent.Runtime, auditSvc *audit.Service) *Handler {
	return &Handler{conv: c, agents: a, runtime: rt, audit: auditSvc}
}

// RegisterUserRoutes mounts the session-protected routes on mux. The
// caller wraps mux with the session middleware and module gate.
func (h *Handler) RegisterUserRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/ai/agents", h.listAgents)
	mux.HandleFunc("GET /api/ai/conversations", h.listConversations)
	mux.HandleFunc("POST /api/ai/conversations", h.createConversation)
	mux.HandleFunc("GET /api/ai/conversations/{id}", h.getConversation)
	mux.HandleFunc("DELETE /api/ai/conversations/{id}", h.deleteConversation)
	mux.HandleFunc("POST /api/ai/conversations/{id}/stream", h.stream)
}

// --- agents ----------------------------------------------------------------

type agentResp struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	Description  string   `json:"description"`
	Model        string   `json:"model"`
	Tools        []string `json:"tools"`
	IsBuiltin    bool     `json:"is_builtin"`
	IsActive     bool     `json:"is_active"`
}

func toAgentResp(a agent.Definition) agentResp {
	tools := a.Tools
	if tools == nil {
		tools = []string{}
	}
	return agentResp{
		ID: a.ID, Name: a.Name, Description: a.Description,
		Model: a.Model, Tools: tools,
		IsBuiltin: a.IsBuiltin, IsActive: a.IsActive,
	}
}

func (h *Handler) listAgents(w http.ResponseWriter, r *http.Request) {
	agents, err := h.agents.List(r.Context(), true)
	if err != nil {
		response.Error(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	out := make([]agentResp, 0, len(agents))
	for _, a := range agents {
		out = append(out, toAgentResp(a))
	}
	response.JSON(w, http.StatusOK, map[string]any{"agents": out})
}

// --- conversations --------------------------------------------------------

type createConvReq struct {
	AgentID string `json:"agent_id"`
	Title   string `json:"title"`
}

type convResp struct {
	ID        string    `json:"id"`
	AgentID   string    `json:"agent_id"`
	Title     string    `json:"title"`
	Model     string    `json:"model"`
	Revision  int64     `json:"revision"`
	CreatedAt string    `json:"created_at"`
	UpdatedAt string    `json:"updated_at"`
}

func toConvResp(c conv.Conversation) convResp {
	return convResp{
		ID: c.ID, AgentID: c.AgentID, Title: c.Title, Model: c.SnapModel,
		Revision: c.Revision,
		CreatedAt: c.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt: c.UpdatedAt.UTC().Format(time.RFC3339),
	}
}

type convDetailResp struct {
	convResp
	System      string         `json:"system"`
	Tools       []string       `json:"tools"`
	Temperature float32        `json:"temperature"`
	MaxTurns    int            `json:"max_turns"`
	Messages    []messageResp  `json:"messages"`
}

type messageResp struct {
	ID               string                 `json:"id"`
	Role             string                 `json:"role"`
	Content          string                 `json:"content"`
	ToolCalls        []provider.ToolCall    `json:"tool_calls,omitempty"`
	ToolCallID       string                 `json:"tool_call_id,omitempty"`
	ToolName         string                 `json:"tool_name,omitempty"`
	PromptTokens     int                    `json:"prompt_tokens,omitempty"`
	CompletionTokens int                    `json:"completion_tokens,omitempty"`
	Seq              int                    `json:"seq"`
	CreatedAt        string                 `json:"created_at"`
}

func toMessageResp(m conv.Message) messageResp {
	out := messageResp{
		ID: m.ID, Role: string(m.Role), Content: m.Content,
		ToolCalls: m.ToolCalls, ToolCallID: m.ToolCallID, ToolName: m.ToolName,
		PromptTokens: m.PromptTokens, CompletionTokens: m.CompletionTokens,
		Seq: m.Seq,
		CreatedAt: m.CreatedAt.UTC().Format(time.RFC3339),
	}
	if out.ToolCalls == nil {
		out.ToolCalls = []provider.ToolCall{}
	}
	return out
}

func (h *Handler) listConversations(w http.ResponseWriter, r *http.Request) {
	sess, ok := requireSession(w, r)
	if !ok {
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	convs, err := h.conv.List(r.Context(), sess.UserID, limit, offset)
	if err != nil {
		response.Error(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	out := make([]convResp, 0, len(convs))
	for _, c := range convs {
		out = append(out, toConvResp(c))
	}
	response.JSON(w, http.StatusOK, map[string]any{"conversations": out})
}

func (h *Handler) createConversation(w http.ResponseWriter, r *http.Request) {
	sess, ok := requireSession(w, r)
	if !ok {
		return
	}
	var req createConvReq
	if err := decodeJSON(r, &req); err != nil {
		response.Error(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	a, err := h.agents.Get(r.Context(), req.AgentID)
	if err != nil {
		if errors.Is(err, agent.ErrNotFound) {
			response.Error(w, http.StatusNotFound, "agent_not_found", "no such agent")
			return
		}
		response.Error(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	if !a.IsActive {
		response.Error(w, http.StatusBadRequest, "agent_inactive", "agent is not active")
		return
	}
	c, err := h.conv.Create(r.Context(), conv.CreateInput{
		UserID: sess.UserID, AgentID: a.ID, Title: req.Title,
		SnapModel: a.Model, SnapSystem: a.SystemPrompt, SnapTools: a.Tools,
		SnapTemperature: a.Temperature, SnapMaxTokens: a.MaxTokens,
		SnapMaxTurns: a.MaxTurns,
	})
	if err != nil {
		writeConvError(w, err)
		return
	}
	h.logAudit(r, "ai.conversation_created", c.ID, map[string]any{"agent_id": a.ID})
	response.JSON(w, http.StatusCreated, toConvResp(c))
}

func (h *Handler) getConversation(w http.ResponseWriter, r *http.Request) {
	sess, ok := requireSession(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	c, msgs, err := h.conv.Get(r.Context(), sess.UserID, id)
	if err != nil {
		writeConvError(w, err)
		return
	}
	out := convDetailResp{
		convResp: toConvResp(c), System: c.SnapSystem, Tools: c.SnapTools,
		Temperature: c.SnapTemperature, MaxTurns: c.SnapMaxTurns,
		Messages: make([]messageResp, 0, len(msgs)),
	}
	for _, m := range msgs {
		out.Messages = append(out.Messages, toMessageResp(m))
	}
	response.JSON(w, http.StatusOK, out)
}

func (h *Handler) deleteConversation(w http.ResponseWriter, r *http.Request) {
	sess, ok := requireSession(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	if err := h.conv.Delete(r.Context(), sess.UserID, id); err != nil {
		writeConvError(w, err)
		return
	}
	h.logAudit(r, "ai.conversation_deleted", id, nil)
	response.JSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// --- stream (SSE chat) ----------------------------------------------------

type streamReq struct {
	Message string `json:"message"`
}

func (h *Handler) stream(w http.ResponseWriter, r *http.Request) {
	sess, ok := requireSession(w, r)
	if !ok {
		return
	}
	if h.runtime == nil {
		response.Error(w, http.StatusServiceUnavailable, "no_provider",
			"no AI provider configured; ask an administrator to add one")
		return
	}
	id := r.PathValue("id")

	// Read the body first so the response can be SSE.
	var req streamReq
	if err := decodeJSON(r, &req); err != nil {
		response.Error(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if strings.TrimSpace(req.Message) == "" {
		response.Error(w, http.StatusBadRequest, "invalid_request", "message required")
		return
	}

	conv0, _, err := h.conv.Get(r.Context(), sess.UserID, id)
	if err != nil {
		writeConvError(w, err)
		return
	}

	sse, ok := NewSSEWriter(w)
	if !ok {
		response.Error(w, http.StatusInternalServerError, "no_stream_support",
			"streaming unsupported")
		return
	}
	stopBeat := sse.Heartbeat(15 * time.Second)
	defer stopBeat()

	spec := agent.Spec{
		Model: conv0.SnapModel, System: conv0.SnapSystem, Tools: conv0.SnapTools,
		Temperature: conv0.SnapTemperature, MaxTokens: conv0.SnapMaxTokens,
		MaxTurns: conv0.SnapMaxTurns,
	}
	events := h.runtime.Run(r.Context(), agent.RunInput{
		UserID: sess.UserID, ConversationID: id,
		UserMessage: req.Message, Spec: spec,
	})
	h.logAudit(r, "ai.message_sent", id, map[string]any{
		"agent_id": conv0.AgentID, "streaming": true,
	})

	for ev := range events {
		switch ev.Kind {
		case agent.EventMetadata:
			_ = sse.Event("metadata", map[string]string{"model": ev.Content})
		case agent.EventDelta:
			_ = sse.Event("delta", map[string]string{"content": ev.Content})
		case agent.EventTool:
			_ = sse.Event("tool", map[string]any{
				"id": ev.ToolCallID, "name": ev.ToolName,
				"args": ev.ToolArgs, "result": ev.ToolResult,
			})
		case agent.EventDone:
			payload := map[string]any{}
			if ev.MessageID != "" {
				payload["message_id"] = ev.MessageID
			}
			if ev.Usage != nil {
				payload["usage"] = ev.Usage
			}
			_ = sse.Event("done", payload)
			return
		case agent.EventError:
			_ = sse.Event("error", ev.Err)
			return
		}
	}
	// Channel closed without an explicit done/error — emit one so the
	// client EventSource always terminates.
	_ = sse.Event("done", map[string]bool{"ok": true})
}

// --- helpers --------------------------------------------------------------

func requireSession(w http.ResponseWriter, r *http.Request) (*session.Session, bool) {
	sess, ok := session.FromContext(r.Context())
	if !ok {
		response.Error(w, http.StatusUnauthorized, "unauthorized", "no active session")
		return nil, false
	}
	return sess, true
}

func (h *Handler) logAudit(r *http.Request, eventType, targetID string, meta map[string]any) {
	if h.audit == nil {
		return
	}
	sess, ok := session.FromContext(r.Context())
	if !ok {
		return
	}
	h.audit.Log(r.Context(), audit.Entry{
		EventType: eventType, ActorUserID: sess.UserID,
		TargetType: "ai", TargetID: targetID,
		IPAddress: clientIP(r), UserAgent: r.UserAgent(),
		Metadata: meta,
	})
}

func writeConvError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, conv.ErrNotFound):
		response.Error(w, http.StatusNotFound, "not_found", "conversation not found")
	case errors.Is(err, conv.ErrInvalidInput):
		response.Error(w, http.StatusBadRequest, "invalid_request", err.Error())
	default:
		response.Error(w, http.StatusInternalServerError, "internal_error", err.Error())
	}
}

const maxJSONBody = 64 << 10 // 64 KiB; chat payloads are small

func decodeJSON(r *http.Request, v any) error {
	ct := r.Header.Get("Content-Type")
	if !strings.Contains(ct, "application/json") {
		return errors.New("content-type must be application/json")
	}
	r.Body = http.MaxBytesReader(nil, r.Body, maxJSONBody)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}

func clientIP(r *http.Request) string {
	if f := r.Header.Get("X-Forwarded-For"); f != "" {
		if i := strings.Index(f, ","); i > 0 {
			return strings.TrimSpace(f[:i])
		}
		return strings.TrimSpace(f)
	}
	return r.RemoteAddr
}
