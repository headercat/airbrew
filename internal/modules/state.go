package modules

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// State stores per-module enabled flags in the server_settings table under
// keys of the form `module.<key>.enabled`. System modules (admin) are always
// enabled and cannot be toggled.
type State struct {
	db *sql.DB
}

// NewState binds a State to db.
func NewState(db *sql.DB) *State { return &State{db: db} }

// IsEnabled reports whether the module is enabled. Unknown settings and
// system modules default to true.
func (s *State) IsEnabled(ctx context.Context, key string) (bool, error) {
	if m, ok := Find(key); ok && m.System {
		return true, nil
	}
	var v string
	err := s.db.QueryRowContext(ctx,
		"SELECT value FROM server_settings WHERE key = ?",
		stateKey(key),
	).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	return v == "true", nil
}

// SetEnabled toggles a module. Returns ErrCannotDisableSystem if the module
// is marked System.
func (s *State) SetEnabled(ctx context.Context, key string, enabled bool) error {
	m, ok := Find(key)
	if !ok {
		return fmt.Errorf("modules: unknown module %q", key)
	}
	if m.System {
		return ErrCannotDisableSystem
	}
	v := "false"
	if enabled {
		v = "true"
	}
	now := time.Now().UTC().Truncate(time.Second)
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO server_settings (key, value, updated_at) VALUES (?, ?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at
	`, stateKey(key), v, now)
	return err
}

// AllEnabled returns a map of module-key → enabled for every module in the
// Catalog, including system modules (always true).
func (s *State) AllEnabled(ctx context.Context) (map[string]bool, error) {
	rows, err := s.db.QueryContext(ctx,
		"SELECT key, value FROM server_settings WHERE key LIKE 'module.%.enabled'",
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	stored := map[string]string{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, v); err != nil {
			return nil, err
		}
		stored[k] = v
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	out := make(map[string]bool, len(Catalog))
	for _, m := range Catalog {
		if m.System {
			out[m.Key] = true
			continue
		}
		v, ok := stored[stateKey(m.Key)]
		if !ok {
			out[m.Key] = true
		} else {
			out[m.Key] = v == "true"
		}
	}
	return out, nil
}

func stateKey(key string) string {
	return "module." + key + ".enabled"
}

// ErrCannotDisableSystem is returned by SetEnabled for system modules.
var ErrCannotDisableSystem = errors.New("modules: system modules cannot be disabled")

// KeyFromSetting reverses stateKey, returning the module key or "" if the
// setting key is not a module-enabled setting.
func KeyFromSetting(settingKey string) string {
	if !strings.HasPrefix(settingKey, "module.") || !strings.HasSuffix(settingKey, ".enabled") {
		return ""
	}
	return strings.TrimSuffix(strings.TrimPrefix(settingKey, "module."), ".enabled")
}
