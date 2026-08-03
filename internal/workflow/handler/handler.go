// Package handler exposes the workflow module HTTP API.
package handler

import (
	"encoding/json"
	"io"
	"net/http"
	"unicode/utf8"

	"github.com/headercat/airbrew/internal/workflow/defn"
	wfexec "github.com/headercat/airbrew/internal/workflow/exec"
	"github.com/headercat/airbrew/internal/workflow/run"
)

// Handler exposes authenticated workflow endpoints and public webhooks.
type Handler struct {
	svc    *run.Service
	engine *wfexec.Engine
}

// New builds a Handler.
func New(svc *run.Service, engine *wfexec.Engine) *Handler {
	return &Handler{svc: svc, engine: engine}
}

// RegisterRoutes mounts authenticated user endpoints.
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/workflow/workflows", h.listWorkflows)
	mux.HandleFunc("POST /api/workflow/workflows", h.createWorkflow)
	mux.HandleFunc("GET /api/workflow/workflows/{id}", h.getWorkflow)
	mux.HandleFunc("PATCH /api/workflow/workflows/{id}", h.patchWorkflow)
	mux.HandleFunc("DELETE /api/workflow/workflows/{id}", h.deleteWorkflow)
	mux.HandleFunc("POST /api/workflow/workflows/{id}/activate", h.activateWorkflow)
	mux.HandleFunc("POST /api/workflow/workflows/{id}/deactivate", h.deactivateWorkflow)
	mux.HandleFunc("POST /api/workflow/workflows/{id}/run", h.runWorkflow)
	mux.HandleFunc("GET /api/workflow/workflows/{id}/versions", h.listVersions)
	mux.HandleFunc("GET /api/workflow/runs", h.listRuns)
	mux.HandleFunc("GET /api/workflow/runs/{id}", h.getRun)
	mux.HandleFunc("GET /api/workflow/runs/{id}/steps", h.listSteps)
	mux.HandleFunc("POST /api/workflow/runs/{id}/cancel", h.cancelRun)
}

// RegisterPublicRoutes mounts public workflow endpoints (status is mounted by
// the module). The webhook trigger is registered separately via
// RegisterWebhookRoute so the caller can wrap it with a per-IP rate limiter.
func (h *Handler) RegisterPublicRoutes(mux *http.ServeMux) {}

// RegisterWebhookRoute mounts the public webhook trigger endpoint.
func (h *Handler) RegisterWebhookRoute(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/workflow/hooks/{token}", h.webhook)
}

type workflowResp struct {
	ID           string          `json:"id"`
	UserID       string          `json:"user_id"`
	Name         string          `json:"name"`
	Description  string          `json:"description"`
	Definition   defn.Definition `json:"definition"`
	Version      int             `json:"version"`
	IsActive     bool            `json:"is_active"`
	TriggerType  string          `json:"trigger_type,omitempty"`
	WebhookToken string          `json:"webhook_token,omitempty"`
	CronExpr     string          `json:"cron_expr,omitempty"`
	CreatedAt    string          `json:"created_at"`
	UpdatedAt    string          `json:"updated_at"`
}

func toWorkflowResp(w *run.Workflow) workflowResp {
	return workflowResp{
		ID: w.ID, UserID: w.UserID, Name: w.Name, Description: w.Description,
		Definition: w.Definition, Version: w.Version, IsActive: w.IsActive,
		TriggerType: w.TriggerType, WebhookToken: w.WebhookToken, CronExpr: w.CronExpr,
		CreatedAt: w.CreatedAt.UTC().Format(timeRFC3339),
		UpdatedAt: w.UpdatedAt.UTC().Format(timeRFC3339),
	}
}

type runResp struct {
	ID         string `json:"id"`
	WorkflowID string `json:"workflow_id"`
	UserID     string `json:"user_id"`
	Version    int    `json:"version"`
	Status     string `json:"status"`
	Trigger    string `json:"trigger"`
	InputJSON  string `json:"input_json"`
	Error      string `json:"error,omitempty"`
	StartedAt  string `json:"started_at"`
	FinishedAt string `json:"finished_at,omitempty"`
}

func toRunResp(r *run.Run) runResp {
	out := runResp{
		ID: r.ID, WorkflowID: r.WorkflowID, UserID: r.UserID, Version: r.Version,
		Status: string(r.Status), Trigger: string(r.Trigger), InputJSON: r.InputJSON,
		Error: r.Error, StartedAt: r.StartedAt.UTC().Format(timeRFC3339),
	}
	if r.FinishedAt != nil {
		out.FinishedAt = r.FinishedAt.UTC().Format(timeRFC3339)
	}
	return out
}

type stepResp struct {
	ID         string `json:"id"`
	RunID      string `json:"run_id"`
	NodeID     string `json:"node_id"`
	NodeType   string `json:"node_type"`
	Status     string `json:"status"`
	InputJSON  string `json:"input_json"`
	OutputJSON string `json:"output_json"`
	Error      string `json:"error,omitempty"`
	DurationMs int64  `json:"duration_ms"`
	Seq        int    `json:"seq"`
	StartedAt  string `json:"started_at"`
	FinishedAt string `json:"finished_at,omitempty"`
}

func toStepResp(s *run.StepRun) stepResp {
	out := stepResp{
		ID: s.ID, RunID: s.RunID, NodeID: s.NodeID, NodeType: s.NodeType,
		Status: string(s.Status), InputJSON: s.InputJSON, OutputJSON: s.OutputJSON,
		Error: s.Error, DurationMs: s.DurationMs, Seq: s.Seq,
		StartedAt: s.StartedAt.UTC().Format(timeRFC3339),
	}
	if s.FinishedAt != nil {
		out.FinishedAt = s.FinishedAt.UTC().Format(timeRFC3339)
	}
	return out
}

func (h *Handler) listWorkflows(w http.ResponseWriter, r *http.Request) {
	sess, ok := requireSession(w, r)
	if !ok {
		return
	}
	items, err := h.svc.List(r.Context(), sess.UserID, parseInt(r.URL.Query().Get("limit")), parseInt(r.URL.Query().Get("offset")))
	if err != nil {
		writeErr(w, err)
		return
	}
	out := make([]workflowResp, 0, len(items))
	for _, item := range items {
		out = append(out, toWorkflowResp(item))
	}
	jsonResp(w, http.StatusOK, map[string]any{"workflows": out})
}

type workflowReq struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Definition  defn.Definition `json:"definition"`
}

func (h *Handler) createWorkflow(w http.ResponseWriter, r *http.Request) {
	sess, ok := requireSession(w, r)
	if !ok {
		return
	}
	var req workflowReq
	if err := decodeJSON(r, &req); err != nil {
		respondErr(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	item, err := h.svc.Create(r.Context(), run.NewWorkflowInput{
		UserID: sess.UserID, Name: req.Name, Description: req.Description, Definition: req.Definition,
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	jsonResp(w, http.StatusCreated, toWorkflowResp(item))
}

func (h *Handler) getWorkflow(w http.ResponseWriter, r *http.Request) {
	sess, ok := requireSession(w, r)
	if !ok {
		return
	}
	item, err := h.svc.Get(r.Context(), sess.UserID, r.PathValue("id"))
	if err != nil {
		writeErr(w, err)
		return
	}
	jsonResp(w, http.StatusOK, toWorkflowResp(item))
}

type patchWorkflowReq struct {
	Name        *string          `json:"name"`
	Description *string          `json:"description"`
	Definition  *defn.Definition `json:"definition"`
}

func (h *Handler) patchWorkflow(w http.ResponseWriter, r *http.Request) {
	sess, ok := requireSession(w, r)
	if !ok {
		return
	}
	var req patchWorkflowReq
	if err := decodeJSON(r, &req); err != nil {
		respondErr(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	item, err := h.svc.Update(r.Context(), sess.UserID, r.PathValue("id"), run.PatchWorkflowInput{
		Name: req.Name, Description: req.Description, Definition: req.Definition,
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	jsonResp(w, http.StatusOK, toWorkflowResp(item))
}

func (h *Handler) deleteWorkflow(w http.ResponseWriter, r *http.Request) {
	sess, ok := requireSession(w, r)
	if !ok {
		return
	}
	if err := h.svc.Delete(r.Context(), sess.UserID, r.PathValue("id")); err != nil {
		writeErr(w, err)
		return
	}
	jsonResp(w, http.StatusOK, map[string]bool{"ok": true})
}

func (h *Handler) activateWorkflow(w http.ResponseWriter, r *http.Request) {
	h.toggleWorkflow(w, r, true)
}

func (h *Handler) deactivateWorkflow(w http.ResponseWriter, r *http.Request) {
	h.toggleWorkflow(w, r, false)
}

func (h *Handler) toggleWorkflow(w http.ResponseWriter, r *http.Request, active bool) {
	sess, ok := requireSession(w, r)
	if !ok {
		return
	}
	var item *run.Workflow
	var err error
	if active {
		item, err = h.svc.Activate(r.Context(), sess.UserID, r.PathValue("id"))
	} else {
		item, err = h.svc.Deactivate(r.Context(), sess.UserID, r.PathValue("id"))
	}
	if err != nil {
		writeErr(w, err)
		return
	}
	jsonResp(w, http.StatusOK, toWorkflowResp(item))
}

type manualRunReq struct {
	Input map[string]any `json:"input"`
}

func (h *Handler) runWorkflow(w http.ResponseWriter, r *http.Request) {
	sess, ok := requireSession(w, r)
	if !ok {
		return
	}
	var req manualRunReq
	if r.Body != nil && r.ContentLength != 0 {
		if err := decodeJSON(r, &req); err != nil {
			respondErr(w, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
	}
	item, err := h.svc.Get(r.Context(), sess.UserID, r.PathValue("id"))
	if err != nil {
		writeErr(w, err)
		return
	}
	// Run asynchronously so a long graph (logic.delay, slow HTTP) is not
	// aborted when the user closes the tab / the browser times out the
	// request. The run row is created synchronously and returned as 202; the
	// SPA can poll the runs view to watch it complete.
	rn, err := h.engine.ExecuteAsync(r.Context(), wfexec.Request{Workflow: item, Trigger: run.RunByManual, Input: req.Input})
	if err != nil && rn == nil {
		writeErr(w, err)
		return
	}
	jsonResp(w, http.StatusAccepted, toRunResp(rn))
}

func (h *Handler) listVersions(w http.ResponseWriter, r *http.Request) {
	sess, ok := requireSession(w, r)
	if !ok {
		return
	}
	items, err := h.svc.ListVersions(r.Context(), sess.UserID, r.PathValue("id"), parseInt(r.URL.Query().Get("limit")), parseInt(r.URL.Query().Get("offset")))
	if err != nil {
		writeErr(w, err)
		return
	}
	jsonResp(w, http.StatusOK, map[string]any{"versions": items})
}

func (h *Handler) listRuns(w http.ResponseWriter, r *http.Request) {
	sess, ok := requireSession(w, r)
	if !ok {
		return
	}
	items, err := h.svc.ListRuns(r.Context(), run.ListRunsFilter{
		UserID: sess.UserID, WorkflowID: r.URL.Query().Get("workflow"),
		Status: run.RunStatus(r.URL.Query().Get("status")),
		Limit:  parseInt(r.URL.Query().Get("limit")), Offset: parseInt(r.URL.Query().Get("offset")),
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	out := make([]runResp, 0, len(items))
	for _, item := range items {
		out = append(out, toRunResp(item))
	}
	jsonResp(w, http.StatusOK, map[string]any{"runs": out})
}

func (h *Handler) getRun(w http.ResponseWriter, r *http.Request) {
	sess, ok := requireSession(w, r)
	if !ok {
		return
	}
	item, err := h.svc.GetRun(r.Context(), sess.UserID, r.PathValue("id"))
	if err != nil {
		writeErr(w, err)
		return
	}
	jsonResp(w, http.StatusOK, toRunResp(item))
}

func (h *Handler) listSteps(w http.ResponseWriter, r *http.Request) {
	sess, ok := requireSession(w, r)
	if !ok {
		return
	}
	items, err := h.svc.ListSteps(r.Context(), sess.UserID, r.PathValue("id"))
	if err != nil {
		writeErr(w, err)
		return
	}
	out := make([]stepResp, 0, len(items))
	for _, item := range items {
		out = append(out, toStepResp(item))
	}
	jsonResp(w, http.StatusOK, map[string]any{"steps": out})
}

func (h *Handler) cancelRun(w http.ResponseWriter, r *http.Request) {
	sess, ok := requireSession(w, r)
	if !ok {
		return
	}
	if err := h.svc.CancelRun(r.Context(), sess.UserID, r.PathValue("id")); err != nil {
		writeErr(w, err)
		return
	}
	jsonResp(w, http.StatusOK, map[string]bool{"ok": true})
}

func (h *Handler) webhook(w http.ResponseWriter, r *http.Request) {
	// Read up to 64 KiB + 1 byte so we can detect (and flag) truncation rather
	// than silently running the workflow against a partial body.
	body, err := io.ReadAll(io.LimitReader(r.Body, 64*1024+1))
	if err != nil {
		respondErr(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	input := map[string]any{
		"body":        string(body),
		"headers":     r.Header,
		"remote_addr": clientIP(r),
	}
	if len(body) > 64*1024 {
		// Trim on a UTF-8 boundary so a multi-byte sequence is not split,
		// which would otherwise produce invalid UTF-8 in $json.body.
		cut := 64 * 1024
		for cut > 0 && !utf8.Valid(body[:cut]) {
			cut--
		}
		input["body"] = string(body[:cut])
		input["body_truncated"] = true
	}
	var parsed any
	if json.Unmarshal(body, &parsed) == nil {
		input["json"] = parsed
	}
	item, err := h.svc.Repo().GetWorkflowByWebhookToken(r.Context(), r.PathValue("token"))
	if err != nil {
		writeErr(w, err)
		return
	}
	// Execute asynchronously: webhook senders (and browsers) routinely time
	// out before a synchronous run completes, then retry and fire the run
	// again. Returning 202 with the run id lets the caller poll for the
	// outcome instead of holding the connection.
	rn, err := h.engine.ExecuteAsync(r.Context(), wfexec.Request{Workflow: item, Trigger: run.RunByWebhook, Input: input})
	if err != nil && rn == nil {
		writeErr(w, err)
		return
	}
	jsonResp(w, http.StatusAccepted, toRunResp(rn))
}
