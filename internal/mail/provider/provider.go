// Package provider persists the admin-selectable mail driver registry
// (mail_providers) and resolves the single active driver per direction.
//
// The stored config is opaque JSON whose shape is defined by each driver in
// internal/mail/inbound and internal/mail/outbound. This package does not
// interpret the config — it only stores it and reports which driver+config is
// active so the driver registries can build the right client.
package provider

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/headercat/airbrew/internal/id"
)

// Direction selects the transport side.
type Direction string

const (
	DirectionInbound  Direction = "inbound"
	DirectionOutbound Direction = "outbound"
)

// Provider is a stored driver configuration.
type Provider struct {
	ID        string
	Direction Direction
	Driver    string
	Config    string // raw JSON
	IsActive  bool
	CreatedAt time.Time
	UpdatedAt time.Time
}

// ErrNotFound is returned when no provider matches the lookup.
var ErrNotFound = errors.New("provider: not found")

// ErrNoActive is returned when no provider is active for a direction.
var ErrNoActive = errors.New("provider: no active driver for direction")

// ErrInvalidInput is returned on shape-validation failure.
var ErrInvalidInput = errors.New("provider: invalid input")

// Repository persists mail_providers.
type Repository struct {
	db *sql.DB
}

// NewRepository returns a Repository bound to db.
func NewRepository(db *sql.DB) *Repository { return &Repository{db: db} }

const providerColumns = `id, direction, driver, config, is_active, created_at, updated_at`

// UpsertAndActivate stores driver config for a direction and marks it the
// single active provider. Any previously active provider for the same direction
// is deactivated in the same transaction.
func (r *Repository) UpsertAndActivate(ctx context.Context, direction Direction, driver, configJSON string) (Provider, error) {
	direction, err := normDirection(direction)
	if err != nil {
		return Provider{}, err
	}
	driver = strings.TrimSpace(driver)
	if driver == "" {
		return Provider{}, fmt.Errorf("%w: driver required", ErrInvalidInput)
	}
	now := time.Now().UTC().Truncate(time.Second)
	p := Provider{ID: id.New(), Direction: direction, Driver: driver, Config: configJSON, IsActive: true, CreatedAt: now, UpdatedAt: now}
	err = inTx(ctx, r.db, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx,
			"UPDATE mail_providers SET is_active = 0, updated_at = ? WHERE direction = ?",
			now, string(direction),
		); err != nil {
			return fmt.Errorf("provider: deactivate previous: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO mail_providers (id, direction, driver, config, is_active, created_at, updated_at)
			VALUES (?, ?, ?, ?, 1, ?, ?)
		`, p.ID, string(direction), driver, configJSON, now, now); err != nil {
			return fmt.Errorf("provider: insert: %w", err)
		}
		return nil
	})
	if err != nil {
		return Provider{}, err
	}
	return p, nil
}

// GetActive returns the active provider for direction, or ErrNoActive.
func (r *Repository) GetActive(ctx context.Context, direction Direction) (Provider, error) {
	direction, err := normDirection(direction)
	if err != nil {
		return Provider{}, err
	}
	row := r.db.QueryRowContext(ctx,
		"SELECT "+providerColumns+" FROM mail_providers WHERE direction = ? AND is_active = 1",
		string(direction))
	p, err := scanProvider(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Provider{}, ErrNoActive
	}
	return p, err
}

// List returns all providers, active first.
func (r *Repository) List(ctx context.Context) ([]Provider, error) {
	rows, err := r.db.QueryContext(ctx,
		"SELECT "+providerColumns+" FROM mail_providers ORDER BY direction, is_active DESC, created_at DESC")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Provider
	for rows.Next() {
		p, err := scanProvider(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// Get returns one provider by ID.
func (r *Repository) Get(ctx context.Context, id string) (Provider, error) {
	row := r.db.QueryRowContext(ctx,
		"SELECT "+providerColumns+" FROM mail_providers WHERE id = ?", id)
	p, err := scanProvider(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Provider{}, ErrNotFound
	}
	return p, err
}

// Delete removes a provider.
func (r *Repository) Delete(ctx context.Context, id string) error {
	res, err := r.db.ExecContext(ctx, "DELETE FROM mail_providers WHERE id = ?", id)
	if err != nil {
		return fmt.Errorf("provider: delete: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func normDirection(d Direction) (Direction, error) {
	switch d {
	case DirectionInbound, DirectionOutbound:
		return d, nil
	}
	return "", fmt.Errorf("%w: bad direction %q", ErrInvalidInput, d)
}

type scanner interface {
	Scan(dest ...any) error
}

func scanProvider(row scanner) (Provider, error) {
	var p Provider
	var dir, driver, cfg string
	var active int
	err := row.Scan(&p.ID, &dir, &driver, &cfg, &active, &p.CreatedAt, &p.UpdatedAt)
	if err != nil {
		return Provider{}, err
	}
	p.Direction = Direction(dir)
	p.Driver = driver
	p.Config = cfg
	p.IsActive = active == 1
	return p, nil
}

func inTx(ctx context.Context, db *sql.DB, fn func(*sql.Tx) error) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("provider: begin tx: %w", err)
	}
	defer tx.Rollback()
	if err := fn(tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("provider: commit: %w", err)
	}
	return nil
}
