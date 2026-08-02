package user

import (
	"context"
	"database/sql"
	"testing"

	_ "modernc.org/sqlite"
)

func TestCountActiveByRoleExcludesSuspendedAdmins(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", "file::memory:?cache=shared&_time_format=sqlite")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.ExecContext(ctx, `
		CREATE TABLE users (
			id TEXT PRIMARY KEY,
			email TEXT NOT NULL,
			display_name TEXT,
			description TEXT,
			birthday DATETIME,
			phone_number TEXT,
			avatar_url TEXT,
			custom_fields TEXT NOT NULL DEFAULT '{}',
			status TEXT NOT NULL,
			role TEXT NOT NULL,
			created_at DATETIME NOT NULL,
			updated_at DATETIME NOT NULL,
			deleted_at DATETIME
		);
		INSERT INTO users (id, email, status, role, created_at, updated_at) VALUES
			('active_admin', 'active@example.test', 'active', 'admin', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
			('suspended_admin', 'suspended@example.test', 'suspended', 'admin', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
			('active_user', 'user@example.test', 'active', 'user', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP);
	`); err != nil {
		t.Fatal(err)
	}
	repo := NewRepository(db)
	admins, err := repo.CountByRole(ctx, RoleAdmin)
	if err != nil {
		t.Fatal(err)
	}
	if admins != 2 {
		t.Fatalf("CountByRole admins = %d, want 2", admins)
	}
	activeAdmins, err := repo.CountActiveByRole(ctx, RoleAdmin)
	if err != nil {
		t.Fatal(err)
	}
	if activeAdmins != 1 {
		t.Fatalf("CountActiveByRole admins = %d, want 1", activeAdmins)
	}
}
