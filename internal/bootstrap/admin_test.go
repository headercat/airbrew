package bootstrap

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/headercat/airbrew/internal/auth/user"
	"github.com/headercat/airbrew/internal/config"

	_ "modernc.org/sqlite"
)

func TestEnsureAdminRecoversExistingInactiveEmail(t *testing.T) {
	ctx := context.Background()
	db := newBootstrapTestDB(t, ctx)
	repo := user.NewRepository(db)
	now := time.Now().UTC().Truncate(time.Second)
	if _, err := db.ExecContext(ctx, `
		INSERT INTO users
		  (id, public_subject, email, status, role, email_verified, custom_fields, created_at, updated_at, deleted_at)
		VALUES (?, ?, ?, ?, ?, 0, '{}', ?, ?, ?)
	`, "existing", "sub_existing", "admin@airbrew.local", string(user.StatusSuspended), string(user.RoleUser), now, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO password_credentials
		  (user_id, password_hash, password_alg, created_at, updated_at, password_changed_at)
		VALUES (?, ?, 'argon2id', ?, ?, ?)
	`, "existing", "old_hash", now, now, now); err != nil {
		t.Fatal(err)
	}

	err := EnsureAdmin(ctx, db, config.Config{BootstrapAdminPassword: "new-password-123"}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	u, err := repo.GetByEmail(ctx, "admin@airbrew.local")
	if err != nil {
		t.Fatal(err)
	}
	if u.ID != "existing" || u.Role != user.RoleAdmin || u.Status != user.StatusActive {
		t.Fatalf("recovered user = %+v", u)
	}
	activeAdmins, err := repo.CountActiveByRole(ctx, user.RoleAdmin)
	if err != nil {
		t.Fatal(err)
	}
	if activeAdmins != 1 {
		t.Fatalf("active admins = %d, want 1", activeAdmins)
	}
}

func newBootstrapTestDB(t *testing.T, ctx context.Context) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file::memory:?cache=shared&_time_format=sqlite")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.ExecContext(ctx, `
		CREATE TABLE users (
			id TEXT PRIMARY KEY,
			public_subject TEXT NOT NULL UNIQUE,
			email TEXT NOT NULL UNIQUE COLLATE NOCASE,
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
		CREATE TABLE password_credentials (
			user_id TEXT PRIMARY KEY,
			password_hash TEXT NOT NULL,
			password_alg TEXT NOT NULL,
			created_at DATETIME NOT NULL,
			updated_at DATETIME NOT NULL,
			password_changed_at DATETIME
		);
		CREATE TABLE audit_logs (
			id TEXT PRIMARY KEY,
			actor_user_id TEXT,
			actor_client_id TEXT,
			event_type TEXT NOT NULL,
			target_type TEXT,
			target_id TEXT,
			ip_address TEXT,
			user_agent TEXT,
			metadata TEXT,
			created_at DATETIME NOT NULL
		);
	`); err != nil {
		t.Fatal(err)
	}
	return db
}
