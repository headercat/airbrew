package exec

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/headercat/airbrew/internal/workflow/defn"
	"github.com/headercat/airbrew/internal/workflow/run"
)

func TestEngineExecutesHTTPAndBranch(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"message":"done"}`))
	}))
	defer api.Close()

	db := testDB(t)
	svc := run.NewService(run.NewRepository(db), nil)
	engine := New(svc, nil)

	def := defn.Definition{
		Nodes: []defn.Node{
			{ID: "start", Type: "trigger.manual"},
			{ID: "fetch", Type: "action.http", Config: raw(map[string]any{"url": api.URL, "method": "GET", "allow_private": true})},
			{ID: "branch", Type: "logic.if", Config: raw(map[string]any{"expr": "{{ $json.status }} eq 200"})},
			{ID: "log", Type: "action.log", Config: raw(map[string]any{"message": "body {{ $json.json.message }}"})},
		},
		Edges: []defn.Edge{
			{From: "start", To: "fetch"},
			{From: "fetch", To: "branch"},
			{From: "branch", To: "log", Port: "true"},
		},
	}
	wf, err := svc.Create(context.Background(), run.NewWorkflowInput{
		UserID: "user_1", Name: "Test", Definition: def,
	})
	if err != nil {
		t.Fatal(err)
	}
	rn, err := engine.Execute(context.Background(), Request{
		Workflow: wf, Trigger: run.RunByManual, Input: map[string]any{"seed": "value"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if rn.Status != run.RunSuccess {
		t.Fatalf("status = %s, want success", rn.Status)
	}
	steps, err := svc.ListSteps(context.Background(), "user_1", rn.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(steps) != 4 {
		t.Fatalf("steps = %d, want 4", len(steps))
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(steps[3].OutputJSON), &out); err != nil {
		t.Fatal(err)
	}
	if out["message"] != "body done" {
		t.Fatalf("message = %#v, want body done", out["message"])
	}
}

func testDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file::memory:?cache=shared&_pragma=foreign_keys(1)&_time_format=sqlite")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	stmts := []string{
		`CREATE TABLE users (id TEXT PRIMARY KEY NOT NULL)`,
		`INSERT INTO users (id) VALUES ('user_1')`,
		`CREATE TABLE workflows (
			id TEXT PRIMARY KEY NOT NULL, user_id TEXT NOT NULL, name TEXT NOT NULL,
			description TEXT NOT NULL DEFAULT '', definition TEXT NOT NULL DEFAULT '{"nodes":[],"edges":[]}',
			version INTEGER NOT NULL DEFAULT 1, is_active INTEGER NOT NULL DEFAULT 0,
			trigger_type TEXT, webhook_token TEXT, cron_expr TEXT,
			created_at DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now')),
			updated_at DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now')),
			FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
		)`,
		`CREATE TABLE workflow_versions (
			id TEXT PRIMARY KEY NOT NULL, workflow_id TEXT NOT NULL, version INTEGER NOT NULL,
			definition TEXT NOT NULL, created_at DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now'))
		)`,
		`CREATE TABLE workflow_runs (
			id TEXT PRIMARY KEY NOT NULL, workflow_id TEXT NOT NULL, user_id TEXT NOT NULL,
			version INTEGER NOT NULL, status TEXT NOT NULL DEFAULT 'pending', trigger TEXT NOT NULL DEFAULT 'manual',
			input_json TEXT NOT NULL DEFAULT '{}', error TEXT NOT NULL DEFAULT '',
			started_at DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now')), finished_at DATETIME
		)`,
		`CREATE TABLE workflow_step_runs (
			id TEXT PRIMARY KEY NOT NULL, run_id TEXT NOT NULL, node_id TEXT NOT NULL, node_type TEXT NOT NULL,
			status TEXT NOT NULL DEFAULT 'pending', input_json TEXT NOT NULL DEFAULT '{}',
			output_json TEXT NOT NULL DEFAULT '{}', error TEXT NOT NULL DEFAULT '',
			duration_ms INTEGER NOT NULL DEFAULT 0, seq INTEGER NOT NULL,
			started_at DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now')), finished_at DATETIME
		)`,
	}
	for _, stmt := range stmts {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	return db
}

func raw(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}
