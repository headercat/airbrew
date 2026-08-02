package admin

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func TestVerifySQLiteFileRequiresAirbrewSchema(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "generic.sqlite")
	db, err := sql.Open("sqlite", "file:"+path+"?_time_format=sqlite")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `CREATE TABLE notes (id TEXT PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	result, err := verifySQLiteFile(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if result.OK {
		t.Fatalf("expected generic sqlite file to fail Airbrew backup checks: %+v", result)
	}
	if result.SchemaCheck == "ok" {
		t.Fatalf("expected schema check to report missing tables")
	}
}

func TestVerifySQLiteFileAcceptsAirbrewCoreSchema(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "airbrew.sqlite")
	db, err := sql.Open("sqlite", "file:"+path+"?_time_format=sqlite")
	if err != nil {
		t.Fatal(err)
	}
	stmts := []string{
		`CREATE TABLE schema_migrations (version TEXT PRIMARY KEY)`,
		`CREATE TABLE users (id TEXT PRIMARY KEY)`,
		`CREATE TABLE sessions (id TEXT PRIMARY KEY)`,
		`CREATE TABLE audit_logs (id TEXT PRIMARY KEY)`,
		`CREATE TABLE module_states (key TEXT PRIMARY KEY)`,
		`CREATE TABLE server_settings (key TEXT PRIMARY KEY)`,
		`INSERT INTO schema_migrations (version) VALUES ('0001_initial.sql')`,
	}
	for _, stmt := range stmts {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	result, err := verifySQLiteFile(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if !result.OK {
		t.Fatalf("expected Airbrew core schema to pass backup checks: %+v", result)
	}
	if result.SchemaCheck != "ok" {
		t.Fatalf("schema check = %q, want ok", result.SchemaCheck)
	}
}

func TestNormalizeLogoURL(t *testing.T) {
	ok := []string{
		"https://example.com/logo.png",
		"http://example.test/logo.svg",
		"/assets/logo.png",
		"  /assets/logo.png  ",
		"",
	}
	for _, raw := range ok {
		if _, err := normalizeLogoURL(raw); err != nil {
			t.Fatalf("normalizeLogoURL(%q) unexpected error: %v", raw, err)
		}
	}

	bad := []string{
		"javascript:alert(1)",
		"data:image/svg+xml;base64,abc",
		"//example.com/logo.png",
		"assets/logo.png",
	}
	for _, raw := range bad {
		if got, err := normalizeLogoURL(raw); err == nil {
			t.Fatalf("normalizeLogoURL(%q) = %q, want error", raw, got)
		}
	}
}

func TestStoredBackupHelpersValidateAndPrune(t *testing.T) {
	dir := t.TempDir()
	names := []string{
		"airbrew-backup-20260101T000000Z.sqlite",
		"airbrew-backup-20260102T000000Z.sqlite",
		"airbrew-backup-20260103T000000Z.sqlite",
		"not-a-backup.sqlite",
	}
	for i, name := range names {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		ts := time.Date(2026, 1, i+1, 0, 0, 0, 0, time.UTC)
		if err := os.Chtimes(path, ts, ts); err != nil {
			t.Fatal(err)
		}
	}
	if validStoredBackupName("../airbrew-backup-20260101T000000Z.sqlite") {
		t.Fatal("path traversal backup name should be invalid")
	}
	if !validStoredBackupName("airbrew-backup-20260101T000000Z.sqlite") {
		t.Fatal("generated backup name should be valid")
	}
	if !validStoredBackupName("airbrew-backup-20260101T000000.123456789Z.sqlite") {
		t.Fatal("nanosecond backup name should be valid")
	}
	first := newStoredBackupName(time.Date(2026, 1, 1, 0, 0, 0, 1, time.UTC))
	second := newStoredBackupName(time.Date(2026, 1, 1, 0, 0, 0, 2, time.UTC))
	if first == second {
		t.Fatal("backup names should include sub-second precision")
	}
	if err := pruneStoredBackups(dir, 2); err != nil {
		t.Fatal(err)
	}
	backups, err := readStoredBackups(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(backups) != 2 {
		t.Fatalf("kept backups = %d, want 2", len(backups))
	}
	if backups[0].Name != "airbrew-backup-20260103T000000Z.sqlite" {
		t.Fatalf("newest backup = %q", backups[0].Name)
	}
	if _, err := os.Stat(filepath.Join(dir, "not-a-backup.sqlite")); err != nil {
		t.Fatal("non-backup files should not be pruned")
	}
}
