package conv

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/headercat/airbrew/internal/ai/provider"
	"github.com/headercat/airbrew/internal/db"
)

func nowUTC() time.Time { return time.Now().UTC() }

// testRepo spins up an in-memory SQLite DB, runs migrations, seeds a user
// and an ai_agents row, and returns a Repository plus the user/agent IDs.
func testRepo(t *testing.T) (*Repository, string, string) {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "conv-test.db"))
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
		VALUES (?, 'Assistant', '', 'gpt-test', '', '[]', 0.7, 0, 6, 1, 1)`,
		agentID,
	); err != nil {
		t.Fatalf("seed agent: %v", err)
	}
	return NewRepository(d.DB), uid, agentID
}

func TestCreateAndGet(t *testing.T) {
	r, uid, agentID := testRepo(t)
	ctx := context.Background()
	c, err := r.Create(ctx, Conversation{
		UserID: uid, AgentID: agentID, Title: "hello",
		SnapModel: "gpt-test", SnapTools: []string{"clock"},
		SnapTemperature: 0.7, SnapMaxTurns: 6,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if c.ID == "" || c.Revision != 1 {
		t.Fatalf("unexpected convo: %+v", c)
	}
	got, err := r.Get(ctx, uid, c.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Title != "hello" || len(got.SnapTools) != 1 || got.SnapTools[0] != "clock" {
		t.Fatalf("snapshot mismatch: %+v", got)
	}
}

func TestOwnershipGuard(t *testing.T) {
	r, uid, agentID := testRepo(t)
	ctx := context.Background()
	c, _ := r.Create(ctx, Conversation{
		UserID: uid, AgentID: agentID, SnapModel: "x", SnapTools: []string{},
		SnapMaxTurns: 6,
	})
	if _, err := r.Get(ctx, "someone-else", c.ID); err != ErrNotFound {
		t.Fatalf("expected ErrNotFound for foreign user, got %v", err)
	}
}

func TestAppendMessageSeq(t *testing.T) {
	r, uid, agentID := testRepo(t)
	ctx := context.Background()
	c, _ := r.Create(ctx, Conversation{
		UserID: uid, AgentID: agentID, SnapModel: "x", SnapTools: []string{},
		SnapMaxTurns: 6,
	})
	m1, err := r.AppendMessage(ctx, uid, Message{
		ConversationID: c.ID, Role: provider.RoleUser, Content: "hi",
	})
	if err != nil {
		t.Fatalf("AppendMessage 1: %v", err)
	}
	m2, err := r.AppendMessage(ctx, uid, Message{
		ConversationID: c.ID, Role: provider.RoleAssistant, Content: "hello",
	})
	if err != nil {
		t.Fatalf("AppendMessage 2: %v", err)
	}
	if m1.Seq != 1 || m2.Seq != 2 {
		t.Fatalf("seq monotonicity: %d, %d", m1.Seq, m2.Seq)
	}
	got, err := r.ListMessages(ctx, uid, c.ID)
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(got))
	}
	// Revision should have advanced.
	after, _ := r.Get(ctx, uid, c.ID)
	if after.Revision != 3 {
		t.Fatalf("expected revision 3 (1 create + 2 appends), got %d", after.Revision)
	}
}

func TestAppendMessageForeignConversation(t *testing.T) {
	r, uid, agentID := testRepo(t)
	ctx := context.Background()
	c, _ := r.Create(ctx, Conversation{
		UserID: uid, AgentID: agentID, SnapModel: "x", SnapTools: []string{},
		SnapMaxTurns: 6,
	})
	// Foreign user attempts to append — must look like "not found" so the
	// ownership boundary is not leaked.
	_, err := r.AppendMessage(ctx, "someone-else", Message{
		ConversationID: c.ID, Role: provider.RoleUser, Content: "pwn",
	})
	if err != ErrNotFound {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestAppendMessageIfRevisionRejectsStaleSnapshot(t *testing.T) {
	r, uid, agentID := testRepo(t)
	ctx := context.Background()
	c, _ := r.Create(ctx, Conversation{
		UserID: uid, AgentID: agentID, SnapModel: "x", SnapTools: []string{},
		SnapMaxTurns: 6,
	})
	if _, err := r.AppendMessageIfRevision(ctx, uid, Message{
		ConversationID: c.ID, Role: provider.RoleUser, Content: "first",
	}, c.Revision); err != nil {
		t.Fatalf("AppendMessageIfRevision first: %v", err)
	}
	if _, err := r.AppendMessageIfRevision(ctx, uid, Message{
		ConversationID: c.ID, Role: provider.RoleUser, Content: "stale",
	}, c.Revision); err != ErrConflict {
		t.Fatalf("expected ErrConflict for stale revision, got %v", err)
	}
}

func TestIncUsageRollup(t *testing.T) {
	r, uid, _ := testRepo(t)
	ctx := context.Background()
	if err := r.IncUsage(ctx, uid, nowUTC(), 100, 50); err != nil {
		t.Fatalf("IncUsage: %v", err)
	}
	if err := r.IncUsage(ctx, uid, nowUTC(), 200, 25); err != nil {
		t.Fatalf("IncUsage 2: %v", err)
	}
	var promptTok, completionTok, reqCount int
	err := r.db.QueryRowContext(ctx,
		"SELECT prompt_tokens, completion_tokens, request_count FROM ai_usage_daily WHERE user_id = ?",
		uid,
	).Scan(&promptTok, &completionTok, &reqCount)
	if err != nil {
		t.Fatalf("QueryRow: %v", err)
	}
	if promptTok != 300 || completionTok != 75 || reqCount != 2 {
		t.Fatalf("rollup wrong: %d/%d/%d", promptTok, completionTok, reqCount)
	}
}

func TestListUsageAggregatesAndFiltersByUser(t *testing.T) {
	r, uid, _ := testRepo(t)
	ctx := context.Background()
	other := uid + "_other"
	if _, err := r.db.ExecContext(ctx,
		`INSERT INTO users (id, email, public_subject, status) VALUES (?, ?, ?, 'active')`,
		other, other+"@example.com", other,
	); err != nil {
		t.Fatalf("seed other user: %v", err)
	}
	day := time.Date(2025, 2, 3, 12, 0, 0, 0, time.UTC)
	if err := r.IncUsage(ctx, uid, day, 100, 20); err != nil {
		t.Fatalf("IncUsage: %v", err)
	}
	if err := r.IncUsage(ctx, other, day, 7, 3); err != nil {
		t.Fatalf("IncUsage other: %v", err)
	}

	all, err := r.ListUsage(ctx, "", day.AddDate(0, 0, -1))
	if err != nil {
		t.Fatalf("ListUsage all: %v", err)
	}
	if len(all) != 1 || all[0].PromptTokens != 107 || all[0].CompletionTokens != 23 || all[0].RequestCount != 2 {
		t.Fatalf("aggregate usage mismatch: %+v", all)
	}

	one, err := r.ListUsage(ctx, uid, day.AddDate(0, 0, -1))
	if err != nil {
		t.Fatalf("ListUsage user: %v", err)
	}
	if len(one) != 1 || one[0].UserID != uid || one[0].PromptTokens != 100 || one[0].CompletionTokens != 20 {
		t.Fatalf("user usage mismatch: %+v", one)
	}
}

func TestHistoryReplay(t *testing.T) {
	svc, uid, agentID := newTestService(t)
	ctx := context.Background()
	_ = agentID
	c, err := svc.Create(ctx, CreateInput{
		UserID: uid, AgentID: agentID, SnapModel: "x", SnapTools: []string{},
		SnapMaxTurns: 6,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := svc.AppendUserMessage(ctx, uid, c.ID, "hello"); err != nil {
		t.Fatalf("AppendUserMessage: %v", err)
	}
	if _, err := svc.AppendAssistantMessage(ctx, uid, c.ID, "world", nil, 5, 3); err != nil {
		t.Fatalf("AppendAssistantMessage: %v", err)
	}
	hist, err := svc.History(ctx, uid, c.ID)
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if len(hist) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(hist))
	}
	if hist[0].Role != provider.RoleUser || hist[1].Role != provider.RoleAssistant {
		t.Fatalf("order wrong: %+v", hist)
	}
	// Token usage lives on the persisted Message, not the provider
	// projection. Re-fetch the assistant message to assert.
	msgs, _ := svc.repo.ListMessages(ctx, uid, c.ID)
	if len(msgs) != 2 || msgs[1].Content != "world" || msgs[1].CompletionTokens != 3 {
		t.Fatalf("assistant row wrong: %+v", msgs)
	}
}

func TestServiceRejectsEmpty(t *testing.T) {
	svc, _, _ := newTestService(t)
	_, err := svc.Create(context.Background(), CreateInput{UserID: "", AgentID: "x"})
	if err == nil || !strings.Contains(err.Error(), "user_id") {
		t.Fatalf("expected user_id error, got %v", err)
	}
}

func TestSoftDeleteHidesFromList(t *testing.T) {
	svc, uid, agentID := newTestService(t)
	ctx := context.Background()
	c, _ := svc.Create(ctx, CreateInput{
		UserID: uid, AgentID: agentID, SnapModel: "x", SnapTools: []string{},
		SnapMaxTurns: 6,
	})
	if err := svc.Delete(ctx, uid, c.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	list, err := svc.List(ctx, uid, 10, 0)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	for _, got := range list {
		if got.ID == c.ID {
			t.Fatalf("soft-deleted conversation appeared in list")
		}
	}
}

func newTestService(t *testing.T) (*Service, string, string) {
	r, uid, agentID := testRepo(t)
	return NewService(r), uid, agentID
}
