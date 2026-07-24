// Package db opens the SQLite database and runs schema migrations.
package db

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

// DB wraps *sql.DB with our SQLite connection.
type DB struct {
	*sql.DB
}

// Open opens a SQLite database at path with WAL mode and foreign keys enabled.
// SetMaxOpenConns(1) is used to avoid write contention under the modernc driver.
// The parent directory is created if missing so relative paths under a fresh
// data dir work without manual setup.
//
// On open, a WAL checkpoint is forced so that any uncommitted WAL data from
// a previous (possibly killed) run is merged into the main database file.
// Without this checkpoint, the bootstrap admin check can return count=0 and
// incorrectly re-create the admin account on every restart.
func Open(path string) (*DB, error) {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("create db dir %q: %w", dir, err)
		}
	}
	dsn := fmt.Sprintf(
		"file:%s?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=wal_autocheckpoint(1000)&_time_format=sqlite",
		path,
	)
	sqlDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite %q: %w", path, err)
	}
	sqlDB.SetMaxOpenConns(1)
	if err := sqlDB.PingContext(context.Background()); err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("ping sqlite: %w", err)
	}
	// Force a checkpoint immediately so any WAL data from a previous run
	// (especially after an unclean shutdown) is merged into the main file.
	_, _ = sqlDB.Exec("PRAGMA wal_checkpoint(TRUNCATE)")
	return &DB{sqlDB}, nil
}

// Close checkpoints the WAL and closes the database connection.
func (d *DB) Close() error {
	_, _ = d.Exec("PRAGMA wal_checkpoint(TRUNCATE)")
	return d.DB.Close()
}
