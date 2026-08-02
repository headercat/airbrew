package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/headercat/airbrew/internal/ai/agent"
	"github.com/headercat/airbrew/internal/ai/conv"
	"github.com/headercat/airbrew/internal/ai/provider"
	"github.com/headercat/airbrew/internal/auth/session"
	"github.com/headercat/airbrew/internal/db"
)

// stubRuntime is a hand-driven agent.Runtime substitute. It emits a fixed
// script of events on each Run.
type stubRuntime struct {
	events []agent.Event
}

func (s *stubRuntime) Run(_ context.Context, _ agent.RunInput) <-chan agent.Event {
	out := make(chan agent.Event, len(s.events))
	go func() {
		defer close(out)
		for _, e := range s.events {
			out <- e
		}
	}()
	return out
}

func TestStreamEmitsSSEFrames(t *testing.T) {
	rt := &stubRuntime{events: []agent.Event{
		{Kind: agent.EventMetadata, Content: "fake-1"},
		{Kind: agent.EventDelta, Content: "Hel"},
		{Kind: agent.EventDelta, Content: "lo"},
		{Kind: agent.EventTool, ToolCallID: "c1", ToolName: "clock", ToolArgs: "{}", ToolResult: "noon"},
		{Kind: agent.EventDone, MessageID: "m1", Usage: &provider.Usage{PromptTokens: 3, CompletionTokens: 2}},
	}}

	svc, uid, agentID := newTestConvService(t)
	conv0, err := svc.Create(context.Background(), conv.CreateInput{
		UserID: uid, AgentID: agentID, SnapModel: "fake-1",
		SnapTools: []string{"clock"}, SnapMaxTurns: 3,
	})
	if err != nil {
		t.Fatalf("create conv: %v", err)
	}

	h := &Handler{conv: svc, runtime: rt}
	req := httptest.NewRequest(http.MethodPost,
		fmt.Sprintf("/api/ai/conversations/%s/stream", conv0.ID),
		strings.NewReader(`{"message":"hi"}`),
	)
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("id", conv0.ID)
	req = req.WithContext(session.WithContext(req.Context(), &session.Session{UserID: uid}))
	rec := httptest.NewRecorder()

	h.stream(rec, req)

	body := rec.Body.String()
	if !strings.Contains(body, "event: delta") {
		t.Fatalf("missing delta frame: %s", body)
	}
	if !strings.Contains(body, `"content":"Hel"`) {
		t.Fatalf("missing first delta content: %s", body)
	}
	if !strings.Contains(body, "event: tool") {
		t.Fatalf("missing tool frame: %s", body)
	}
	if !strings.Contains(body, "event: done") {
		t.Fatalf("missing done frame: %s", body)
	}
	if !strings.Contains(body, `"message_id":"m1"`) {
		t.Fatalf("missing message id in done: %s", body)
	}
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(line, "data: ") {
			payload := strings.TrimPrefix(line, "data: ")
			if !json.Valid([]byte(payload)) {
				t.Fatalf("invalid JSON frame: %s", payload)
			}
		}
	}
}

func TestStreamClosedWithoutDoneEmitsError(t *testing.T) {
	rt := &stubRuntime{events: []agent.Event{
		{Kind: agent.EventMetadata, Content: "fake-1"},
	}}
	svc, uid, agentID := newTestConvService(t)
	conv0, err := svc.Create(context.Background(), conv.CreateInput{
		UserID: uid, AgentID: agentID, SnapModel: "fake-1",
	})
	if err != nil {
		t.Fatalf("create conv: %v", err)
	}
	h := &Handler{conv: svc, runtime: rt}
	req := httptest.NewRequest(http.MethodPost,
		fmt.Sprintf("/api/ai/conversations/%s/stream", conv0.ID),
		strings.NewReader(`{"message":"hi"}`),
	)
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("id", conv0.ID)
	req = req.WithContext(session.WithContext(req.Context(), &session.Session{UserID: uid}))
	rec := httptest.NewRecorder()

	h.stream(rec, req)

	body := rec.Body.String()
	if strings.Contains(body, "event: done") {
		t.Fatalf("unexpected synthetic done frame: %s", body)
	}
	if !strings.Contains(body, "event: error") || !strings.Contains(body, `"code":"stream_closed"`) {
		t.Fatalf("missing stream_closed error frame: %s", body)
	}
}

func TestStreamWaitsBrieflyForAutoTitle(t *testing.T) {
	rt := &stubRuntime{events: []agent.Event{
		{Kind: agent.EventMetadata, Content: "fake-1"},
		{Kind: agent.EventDone, MessageID: "m1"},
	}}

	svc, uid, agentID := newTestConvService(t)
	conv0, err := svc.Create(context.Background(), conv.CreateInput{
		UserID: uid, AgentID: agentID, SnapModel: "fake-1",
	})
	if err != nil {
		t.Fatalf("create conv: %v", err)
	}

	h := (&Handler{conv: svc, runtime: rt}).WithAutoTitle(
		func(ctx context.Context, userID, conversationID, userMessage string) (string, error) {
			select {
			case <-time.After(25 * time.Millisecond):
				return "Delayed Title", nil
			case <-ctx.Done():
				return "", ctx.Err()
			}
		},
	)
	req := httptest.NewRequest(http.MethodPost,
		fmt.Sprintf("/api/ai/conversations/%s/stream", conv0.ID),
		strings.NewReader(`{"message":"hi"}`),
	)
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("id", conv0.ID)
	req = req.WithContext(session.WithContext(req.Context(), &session.Session{UserID: uid}))
	rec := httptest.NewRecorder()

	h.stream(rec, req)

	body := rec.Body.String()
	if !strings.Contains(body, `"title":"Delayed Title"`) {
		t.Fatalf("missing title in done frame: %s", body)
	}
}

func TestStreamRequiresMessage(t *testing.T) {
	h := &Handler{runtime: &stubRuntime{}}
	req := httptest.NewRequest(http.MethodPost,
		"/api/ai/conversations/x/stream",
		strings.NewReader(`{"message":""}`),
	)
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(session.WithContext(req.Context(), &session.Session{UserID: "u1"}))
	rec := httptest.NewRecorder()
	h.stream(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

func TestAdminListTools(t *testing.T) {
	reg := agent.NewToolRegistry()
	agent.Builtin(reg)
	h := &AdminHandler{tools: reg}

	req := httptest.NewRequest(http.MethodGet, "/api/admin/ai/tools", nil)
	rec := httptest.NewRecorder()
	h.listTools(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var body struct {
		Tools []struct {
			Key  string `json:"key"`
			Name string `json:"name"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode tools: %v", err)
	}
	if len(body.Tools) != 2 {
		t.Fatalf("expected 2 tools, got %+v", body.Tools)
	}
	if body.Tools[0].Key != "clock" || body.Tools[1].Key != "echo" {
		t.Fatalf("tools not sorted: %+v", body.Tools)
	}
	if body.Tools[0].Name == "" || body.Tools[1].Name == "" {
		t.Fatalf("tool schema names missing: %+v", body.Tools)
	}
}

func TestAdminRejectsUnknownAgentTool(t *testing.T) {
	reg := agent.NewToolRegistry()
	agent.Builtin(reg)
	h := &AdminHandler{tools: reg}

	err := h.validateAgentTools([]string{"clock", "missing.tool"})
	if err == nil {
		t.Fatalf("expected unknown tool error")
	}
	if !strings.Contains(err.Error(), "missing.tool") {
		t.Fatalf("error should name unknown tool: %v", err)
	}
}

// newTestConvService spins up an in-memory conv.Service backed by SQLite.
func newTestConvService(t *testing.T) (*conv.Service, string, string) {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "handler-test.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	if err := d.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	uid := "u_" + t.Name()
	if _, err := d.DB.ExecContext(context.Background(),
		`INSERT INTO users (id, email, public_subject, status) VALUES (?, ?, ?, 'active')`,
		uid, uid+"@example.com", uid,
	); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	agentID := "agent-default"
	if _, err := d.DB.ExecContext(context.Background(), `
		INSERT INTO ai_agents (id, name, description, model, system_prompt, tools,
		  temperature, max_tokens, max_turns, is_builtin, is_active)
		VALUES (?, 'Assistant', '', 'fake-1', '', '[]', 0.7, 0, 6, 1, 1)`,
		agentID,
	); err != nil {
		t.Fatalf("seed agent: %v", err)
	}
	return conv.NewService(conv.NewRepository(d.DB)), uid, agentID
}
