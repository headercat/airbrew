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
	"context"
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
	"github.com/headercat/airbrew/internal/httpserver/requestip"
	"github.com/headercat/airbrew/internal/httpserver/response"
)

// Runtime is the contract Handler needs from the agent runtime. The
// production *agent.Runtime satisfies it; tests can substitute their own.
type Runtime interface {
	Run(ctx context.Context, in agent.RunInput) <-chan agent.Event
}

// Handler exposes the user-facing AI endpoints.
type Handler struct {
	conv    *conv.Service
	agents  *agent.DefinitionRepo
	runtime Runtime
	audit   *audit.Service
	// autoTitle is the optional Module hook that generates a short title
	// after the first user message. nil leaves chat without auto-titles.
	autoTitle func(ctx context.Context, userID, conversationID, userMessage string) (string, error)
}

// New builds a Handler. runtime may be nil when providers are not
// configured; in that case chat returns a friendly 503.
func New(c *conv.Service, a *agent.DefinitionRepo, rt Runtime, auditSvc *audit.Service) *Handler {
	return &Handler{conv: c, agents: a, runtime: rt, audit: auditSvc}
}

// WithAutoTitle wires the Module-level AutoTitle hook. Streams whose
// conversation has no title yet will trigger a background title
// generation that emits an extra "title" event when it succeeds.
func (h *Handler) WithAutoTitle(fn func(ctx context.Context, userID, conversationID, userMessage string) (string, error)) *Handler {
	h.autoTitle = fn
	return h
}

// RegisterUserRoutes mounts the session-protected routes on mux. The
// caller wraps mux with the session middleware and module gate.
func (h *Handler) RegisterUserRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/ai/agents", h.listAgents)
	mux.HandleFunc("GET /api/ai/conversations", h.listConversations)
	mux.HandleFunc("POST /api/ai/conversations", h.createConversation)
	mux.HandleFunc("GET /api/ai/conversations/{id}", h.getConversation)
	mux.HandleFunc("PATCH /api/ai/conversations/{id}", h.patchConversation)
	mux.HandleFunc("DELETE /api/ai/conversations/{id}", h.deleteConversation)
	mux.HandleFunc("POST /api/ai/conversations/{id}/stream", h.stream)
}

// --- agents ----------------------------------------------------------------

type agentResp struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Model       string   `json:"model"`
	Tools       []string `json:"tools"`
	IsBuiltin   bool     `json:"is_builtin"`
	IsActive    bool     `json:"is_active"`
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
		sanitizeInternal(w, err)
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
	ID        string `json:"id"`
	AgentID   string `json:"agent_id"`
	Title     string `json:"title"`
	Model     string `json:"model"`
	Revision  int64  `json:"revision"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

func toConvResp(c conv.Conversation) convResp {
	return convResp{
		ID: c.ID, AgentID: c.AgentID, Title: c.Title, Model: c.SnapModel,
		Revision:  c.Revision,
		CreatedAt: c.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt: c.UpdatedAt.UTC().Format(time.RFC3339),
	}
}

type convDetailResp struct {
	convResp
	System      string        `json:"system"`
	Tools       []string      `json:"tools"`
	Temperature float32       `json:"temperature"`
	MaxTurns    int           `json:"max_turns"`
	Messages    []messageResp `json:"messages"`
}

type messageResp struct {
	ID               string              `json:"id"`
	Role             string              `json:"role"`
	Content          string              `json:"content"`
	ToolCalls        []provider.ToolCall `json:"tool_calls,omitempty"`
	ToolCallID       string              `json:"tool_call_id,omitempty"`
	ToolName         string              `json:"tool_name,omitempty"`
	PromptTokens     int                 `json:"prompt_tokens,omitempty"`
	CompletionTokens int                 `json:"completion_tokens,omitempty"`
	Seq              int                 `json:"seq"`
	CreatedAt        string              `json:"created_at"`
}

func toMessageResp(m conv.Message) messageResp {
	out := messageResp{
		ID: m.ID, Role: string(m.Role), Content: m.Content,
		ToolCalls: m.ToolCalls, ToolCallID: m.ToolCallID, ToolName: m.ToolName,
		PromptTokens: m.PromptTokens, CompletionTokens: m.CompletionTokens,
		Seq:       m.Seq,
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
		sanitizeInternal(w, err)
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
		sanitizeInternal(w, err)
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

type patchConvReq struct {
	Title *string `json:"title"`
}

func (h *Handler) patchConversation(w http.ResponseWriter, r *http.Request) {
	sess, ok := requireSession(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	var req patchConvReq
	if err := decodeJSON(r, &req); err != nil {
		response.Error(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if req.Title == nil {
		response.Error(w, http.StatusBadRequest, "invalid_request", "no fields to update")
		return
	}
	if err := h.conv.SetTitle(r.Context(), sess.UserID, id, *req.Title); err != nil {
		writeConvError(w, err)
		return
	}
	c, _, err := h.conv.Get(r.Context(), sess.UserID, id)
	if err != nil {
		writeConvError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, toConvResp(c))
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
	stopBeat, heartbeatErrs := sse.Heartbeat(15 * time.Second)
	defer stopBeat()

	spec := agent.Spec{
		Model: conv0.SnapModel, System: conv0.SnapSystem, Tools: conv0.SnapTools,
		Temperature: conv0.SnapTemperature, MaxTokens: conv0.SnapMaxTokens,
		MaxTurns: conv0.SnapMaxTurns,
	}
	events := h.runtime.Run(r.Context(), agent.RunInput{
		UserID: sess.UserID, ConversationID: id, ExpectedRevision: conv0.Revision,
		UserMessage: req.Message, Spec: spec,
	})
	h.logAudit(r, "ai.message_sent", id, map[string]any{
		"agent_id": conv0.AgentID, "streaming": true,
	})

	// If the conversation is still untitled, fire a background title
	// generation as soon as the user message has been persisted. The
	// generated title is emitted as an out-of-band event.
	needTitle := h.autoTitle != nil && conv0.Title == ""
	var titleCh chan string
	if needTitle {
		titleCh = make(chan string, 1)
		bgCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		go func() {
			defer cancel()
			defer close(titleCh)
			t, err := h.autoTitle(bgCtx, sess.UserID, id, req.Message)
			if err != nil {
				return
			}
			select {
			case titleCh <- t:
			case <-bgCtx.Done():
			}
		}()
	}

	for {
		select {
		case err, ok := <-heartbeatErrs:
			if ok && err != nil {
				return
			}
			heartbeatErrs = nil
			continue
		case ev, ok := <-events:
			if !ok {
				// Channel closed without an explicit done/error. Treat this as a
				// failed stream so optimistic client state reconciles from storage.
				_ = sse.Event("error", map[string]string{
					"code":        "stream_closed",
					"description": "agent stream closed before done",
				})
				return
			}

			switch ev.Kind {
			case agent.EventMetadata:
				if err := sse.Event("metadata", map[string]string{"model": ev.Content}); err != nil {
					return
				}
			case agent.EventDelta:
				if err := sse.Event("delta", map[string]string{"content": ev.Content}); err != nil {
					return
				}
			case agent.EventToolStart:
				if err := sse.Event("tool_start", map[string]any{
					"id": ev.ToolCallID, "name": ev.ToolName, "args": ev.ToolArgs,
				}); err != nil {
					return
				}
			case agent.EventTool:
				if err := sse.Event("tool", map[string]any{
					"id": ev.ToolCallID, "name": ev.ToolName,
					"args": ev.ToolArgs, "result": ev.ToolResult,
				}); err != nil {
					return
				}
			case agent.EventDone:
				payload := map[string]any{}
				if ev.MessageID != "" {
					payload["message_id"] = ev.MessageID
				}
				if ev.Usage != nil {
					payload["usage"] = ev.Usage
				}
				// Drain a pending title result before closing the stream so
				// the SPA receives the rename atomically with the done event.
				if titleCh != nil {
					select {
					case t := <-titleCh:
						if t != "" {
							payload["title"] = t
						}
					case <-time.After(750 * time.Millisecond):
					}
				}
				if err := sse.Event("done", payload); err != nil {
					return
				}
				return
			case agent.EventError:
				if err := sse.Event("error", ev.Err); err != nil {
					return
				}
				return
			}
		}
	}
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
	case errors.Is(err, conv.ErrConflict):
		response.Error(w, http.StatusConflict, "conflict",
			"another message is being sent; please retry")
	case errors.Is(err, conv.ErrInvalidInput):
		// Input errors are user-facing and safe to echo.
		response.Error(w, http.StatusBadRequest, "invalid_request", err.Error())
	default:
		// Internal errors may include DB paths or provider details —
		// log full detail and return a generic message.
		sanitizeInternal(w, err)
	}
}

// sanitizeInternal writes a 500 with a generic message and relies on
// the access-log middleware to capture the underlying error.
func sanitizeInternal(w http.ResponseWriter, err error) {
	response.Error(w, http.StatusInternalServerError, "internal_error",
		"internal error; see server logs")
	_ = err
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
	return requestip.DirectClientIP(r)
}
