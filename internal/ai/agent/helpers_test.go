package agent

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/headercat/airbrew/internal/ai/conv"
	"github.com/headercat/airbrew/internal/db"
)

// newTestService spins up a conv.Service backed by an in-memory SQLite DB
// with migrations applied, a user seeded, and a built-in ai_agents row
// inserted. Returns the service + the seeded user ID + agent ID.
func newTestService(t *testing.T) (*conv.Service, string, string) {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "agent-test.db"))
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

// mustService returns a conv.Service without seeding; used for tests that
// only need the service type and don't care about rows.
func mustService(t *testing.T) *conv.Service {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "agent-svc.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	if err := d.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return conv.NewService(conv.NewRepository(d.DB))
}
