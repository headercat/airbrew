package user

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	_ "modernc.org/sqlite"
)

func TestCountActiveByRoleExcludesSuspendedAdmins(t *testing.T) {
	ctx := context.Background()
	db := newUserRepoTestDB(t, ctx)
	if _, err := db.ExecContext(ctx, `
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

func TestSetRoleAndStatusPreserveLastActiveAdmin(t *testing.T) {
	ctx := context.Background()
	db := newUserRepoTestDB(t, ctx)
	if _, err := db.ExecContext(ctx, `
		INSERT INTO users (id, email, status, role, created_at, updated_at) VALUES
			('admin1', 'one@example.test', 'active', 'admin', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
			('admin2', 'two@example.test', 'active', 'admin', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP);
	`); err != nil {
		t.Fatal(err)
	}
	repo := NewRepository(db)
	if err := repo.SetRolePreservingActiveAdmin(ctx, "admin1", RoleUser); err != nil {
		t.Fatal(err)
	}
	if err := repo.SetStatusPreservingActiveAdmin(ctx, "admin2", StatusSuspended); !errors.Is(err, ErrLastActiveAdmin) {
		t.Fatalf("SetStatusPreservingActiveAdmin err = %v, want ErrLastActiveAdmin", err)
	}
	if err := repo.SetRolePreservingActiveAdmin(ctx, "admin2", RoleUser); !errors.Is(err, ErrLastActiveAdmin) {
		t.Fatalf("SetRolePreservingActiveAdmin err = %v, want ErrLastActiveAdmin", err)
	}
}

func TestGetByEmailIsCaseInsensitive(t *testing.T) {
	ctx := context.Background()
	db := newUserRepoTestDB(t, ctx)
	if _, err := db.ExecContext(ctx, `
		INSERT INTO users (id, email, status, role, created_at, updated_at) VALUES
			('u1', 'alice@example.test', 'active', 'user', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP);
	`); err != nil {
		t.Fatal(err)
	}
	repo := NewRepository(db)
	for _, in := range []string{"alice@example.test", "Alice@Example.Test", "ALICE@EXAMPLE.TEST"} {
		u, err := repo.GetByEmail(ctx, in)
		if err != nil {
			t.Fatalf("GetByEmail(%q): %v", in, err)
		}
		if u.ID != "u1" {
			t.Fatalf("GetByEmail(%q) = %s, want u1", in, u.ID)
		}
	}
}

func newUserRepoTestDB(t *testing.T, ctx context.Context) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file::memory:?cache=shared&_time_format=sqlite")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.ExecContext(ctx, `
		CREATE TABLE users (
			id TEXT PRIMARY KEY,
			public_subject TEXT NOT NULL DEFAULT '',
			email TEXT NOT NULL,
			email_verified INTEGER NOT NULL DEFAULT 0,
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
	`); err != nil {
		t.Fatal(err)
	}
	return db
}
