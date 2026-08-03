package run

import (
	"context"
	"database/sql"
	"testing"

	_ "modernc.org/sqlite"
)

// TestReapStaleRunning verifies runs left in "running" by a crashed process
// are surfaced as failed on the next startup.
func TestReapStaleRunning(t *testing.T) {
	db := newRunTestDB(t)
	repo := NewRepository(db)
	ctx := context.Background()

	// Seed two runs stuck in "running" and one already finished.
	mk := func(id, status string) {
		if _, err := db.Exec(`INSERT INTO workflow_runs (id, workflow_id, user_id, version, status, trigger)
			VALUES (?, 'wf1', 'user_1', 1, ?, 'manual')`, id, status); err != nil {
			t.Fatal(err)
		}
	}
	mk("r1", string(RunRunning))
	mk("r2", string(RunRunning))
	mk("r3", string(RunSuccess))

	n, err := repo.ReapStaleRunning(ctx)
	if err != nil {
		t.Fatalf("reap: %v", err)
	}
	if n != 2 {
		t.Fatalf("reaped = %d, want 2", n)
	}
	// A second pass is a no-op.
	if n, _ := repo.ReapStaleRunning(ctx); n != 0 {
		t.Fatalf("second reap = %d, want 0", n)
	}
	statusOf := func(id string) string {
		var s string
		if err := db.QueryRow(`SELECT status FROM workflow_runs WHERE id = ?`, id).Scan(&s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	if statusOf("r1") != string(RunFailed) || statusOf("r2") != string(RunFailed) {
		t.Fatal("stale runs not marked failed")
	}
	if statusOf("r3") != string(RunSuccess) {
		t.Fatal("finished run should not be touched")
	}
}

func newRunTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file::memory:?cache=shared&_time_format=sqlite")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`CREATE TABLE workflow_runs (
		id TEXT PRIMARY KEY NOT NULL, workflow_id TEXT NOT NULL, user_id TEXT NOT NULL,
		version INTEGER NOT NULL DEFAULT 1, status TEXT NOT NULL, trigger TEXT NOT NULL DEFAULT 'manual',
		input_json TEXT NOT NULL DEFAULT '{}', error TEXT NOT NULL DEFAULT '',
		started_at DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now')),
		finished_at DATETIME
	)`); err != nil {
		t.Fatal(err)
	}
	return db
}
