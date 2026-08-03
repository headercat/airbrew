package session

import (
	"context"
	"database/sql"
	"testing"

	_ "modernc.org/sqlite"
)

func TestRevokeAllForUserExcept(t *testing.T) {
	ctx := context.Background()
	db := newSessionTestDB(t, ctx)
	_, err := db.ExecContext(ctx, `
		INSERT INTO sessions (id, user_id, session_token_hash, created_at, expires_at) VALUES
			('keep',   'u1', 'h1', '2020-01-01', '2999-01-01'),
			('other1', 'u1', 'h2', '2020-01-01', '2999-01-01'),
			('other2', 'u1', 'h3', '2020-01-01', '2999-01-01'),
			('revoked','u1', 'h4', '2020-01-01', '2999-01-01'),
			('expired','u1', 'h5', '2020-01-01', '2000-01-01'),
			('otheru', 'u2', 'h6', '2020-01-01', '2999-01-01');
	`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx,
		`UPDATE sessions SET revoked_at = '2021-01-01' WHERE id = 'revoked'`); err != nil {
		t.Fatal(err)
	}
	repo := NewRepository(db)
	n, err := repo.RevokeAllForUserExcept(ctx, "u1", "keep")
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("revoked = %d, want 2 (only active unexpired others)", n)
	}
	// 'keep' must remain active; 'revoked'/'expired' untouched; 'u2' untouched.
	active := func(id string) bool {
		var revoked sql.NullString
		if err := db.QueryRowContext(ctx,
			`SELECT revoked_at FROM sessions WHERE id = ?`, id).Scan(&revoked); err != nil {
			t.Fatal(err)
		}
		return !revoked.Valid
	}
	if !active("keep") {
		t.Fatal("keep session was revoked")
	}
	if active("other1") || active("other2") {
		t.Fatal("other active sessions were not revoked")
	}
	if !active("otheru") {
		t.Fatal("a different user's session was revoked")
	}
}

func newSessionTestDB(t *testing.T, ctx context.Context) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file::memory:?cache=shared&_time_format=sqlite")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.ExecContext(ctx, `
		CREATE TABLE sessions (
			id TEXT PRIMARY KEY,
			user_id TEXT NOT NULL,
			session_token_hash TEXT NOT NULL,
			ip_address TEXT,
			user_agent TEXT,
			created_at DATETIME NOT NULL,
			expires_at DATETIME NOT NULL,
			revoked_at DATETIME
		);
	`); err != nil {
		t.Fatal(err)
	}
	return db
}
