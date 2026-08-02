// Package provider — registry.go
//
// The registry persists ai_provider_configs rows and resolves the active
// driver+config into a ready-to-use LLMClient. API keys are sealed by the
// ai/crypto.Seal so that a database leak alone cannot recover them.
package provider

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/headercat/airbrew/internal/ai/crypto"
	"github.com/headercat/airbrew/internal/id"
)

// StoredProvider is the persisted form of an ai_provider_configs row. The
// handler returns it verbatim to admin users; the API key fields are
// blanked first.
type StoredProvider struct {
	ID         string    `json:"id"`
	Direction  Direction `json:"direction"`
	Driver     string    `json:"driver"`
	Name       string    `json:"name"`
	BaseURL    string    `json:"base_url"`
	ModelHint  string    `json:"model_hint"`
	APIKeyEnc  string    `json:"-"` // never serialized
	APIKeyNonce string   `json:"-"`
	IsActive   bool      `json:"is_active"`
	HasAPIKey  bool      `json:"has_api_key"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// ErrNotFound is returned when no provider matches the lookup.
var ErrNotFound = errors.New("provider: not found")

// ErrNoActive is returned when no provider is active for a direction.
var ErrNoActive = errors.New("provider: no active driver for direction")

// ErrInvalidInput is returned on shape-validation failure.
var ErrInvalidInput = errors.New("provider: invalid input")

// Repository persists ai_provider_configs.
type Repository struct {
	db   *sql.DB
	seal *crypto.Seal
}

// NewRepository returns a Repository bound to db. seal may be nil; in that
// case API keys are stored and returned as-is (test-only convenience).
func NewRepository(db *sql.DB, seal *crypto.Seal) *Repository {
	return &Repository{db: db, seal: seal}
}

const providerColumns = `id, direction, driver, name, base_url, model_hint,
api_key_enc, api_key_nonce, is_active, created_at, updated_at`

// Put stores driver config for a direction and marks it the single active
// provider. Any previously active provider for the same direction is
// deactivated in the same transaction.
//
// apiKey may be empty for drivers that do not require one (e.g. local
// Ollama). When non-empty and a seal is configured, it is encrypted before
// storage.
func (r *Repository) Put(ctx context.Context, direction Direction, driver, name, baseURL, model, apiKey string) (StoredProvider, error) {
	dir, err := normDirection(direction)
	if err != nil {
		return StoredProvider{}, err
	}
	driver = strings.TrimSpace(driver)
	if driver == "" {
		return StoredProvider{}, fmt.Errorf("%w: driver required", ErrInvalidInput)
	}
	if _, ok := LookupDriver(driver); !ok {
		return StoredProvider{}, fmt.Errorf("%w: unknown driver %q", ErrInvalidInput, driver)
	}
	name = strings.TrimSpace(name)
	if name == "" {
		name = driver
	}

	keyEnc, nonceEnc := "", ""
	if apiKey != "" && r.seal != nil {
		keyEnc, nonceEnc, err = r.seal.EncryptString(apiKey)
		if err != nil {
			return StoredProvider{}, err
		}
	} else if apiKey != "" {
		// No seal configured: store as-is (test mode only).
		keyEnc, nonceEnc = "plain:"+apiKey, "0"
	}

	now := time.Now().UTC().Truncate(time.Second)
	p := StoredProvider{
		ID: id.New(), Direction: dir, Driver: driver, Name: name,
		BaseURL: baseURL, ModelHint: model,
		APIKeyEnc: keyEnc, APIKeyNonce: nonceEnc,
		IsActive: true, CreatedAt: now, UpdatedAt: now,
		HasAPIKey: apiKey != "",
	}
	err = inTx(ctx, r.db, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx,
			"UPDATE ai_provider_configs SET is_active = 0, updated_at = ? WHERE direction = ?",
			now, string(dir),
		); err != nil {
			return fmt.Errorf("provider: deactivate previous: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO ai_provider_configs
			  (id, direction, driver, name, base_url, model_hint,
			   api_key_enc, api_key_nonce, is_active, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, 1, ?, ?)
		`, p.ID, string(dir), driver, name, baseURL, model,
			keyEnc, nonceEnc, now, now,
		); err != nil {
			return fmt.Errorf("provider: insert: %w", err)
		}
		return nil
	})
	if err != nil {
		return StoredProvider{}, err
	}
	return p, nil
}

// GetActive returns the active provider for direction, or ErrNoActive.
func (r *Repository) GetActive(ctx context.Context, direction Direction) (StoredProvider, error) {
	dir, err := normDirection(direction)
	if err != nil {
		return StoredProvider{}, err
	}
	row := r.db.QueryRowContext(ctx,
		"SELECT "+providerColumns+" FROM ai_provider_configs WHERE direction = ? AND is_active = 1",
		string(dir))
	p, err := scanProvider(row)
	if errors.Is(err, sql.ErrNoRows) {
		return StoredProvider{}, ErrNoActive
	}
	return p, err
}

// List returns all providers, active first.
func (r *Repository) List(ctx context.Context) ([]StoredProvider, error) {
	rows, err := r.db.QueryContext(ctx,
		"SELECT "+providerColumns+" FROM ai_provider_configs ORDER BY direction, is_active DESC, created_at DESC")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []StoredProvider
	for rows.Next() {
		p, err := scanProvider(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// Delete removes a provider by ID.
func (r *Repository) Delete(ctx context.Context, id string) error {
	res, err := r.db.ExecContext(ctx, "DELETE FROM ai_provider_configs WHERE id = ?", id)
	if err != nil {
		return fmt.Errorf("provider: delete: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// Resolve builds the active LLMClient for the given direction. Returns
// ErrNoActive if no provider is configured.
func (r *Repository) Resolve(ctx context.Context, direction Direction) (LLMClient, error) {
	p, err := r.GetActive(ctx, direction)
	if err != nil {
		return nil, err
	}
	apiKey := p.APIKeyEnc
	if r.seal != nil && p.APIKeyEnc != "" && p.APIKeyNonce != "" {
		dec, err := r.seal.DecryptString(p.APIKeyEnc, p.APIKeyNonce)
		if err != nil {
			return nil, fmt.Errorf("provider: decrypt api key: %w", err)
		}
		apiKey = dec
	} else if strings.HasPrefix(p.APIKeyEnc, "plain:") {
		apiKey = strings.TrimPrefix(p.APIKeyEnc, "plain:")
	}
	cfg := Config{
		Direction: p.Direction, Driver: p.Driver, BaseURL: p.BaseURL,
		Model: p.ModelHint, APIKey: apiKey,
	}
	cli, err := Build(cfg)
	if err != nil {
		return nil, err
	}
	return cli, nil
}

// SafeForJSON returns a copy of p with secret fields blanked, suitable for
// serialization to admin or user clients. has_api_key is preserved.
func SafeForJSON(p StoredProvider) StoredProvider {
	p.APIKeyEnc = ""
	p.APIKeyNonce = ""
	return p
}

func normDirection(d Direction) (Direction, error) {
	switch d {
	case DirectionChat, DirectionEmbed:
		return d, nil
	}
	return "", fmt.Errorf("%w: bad direction %q", ErrInvalidInput, d)
}

type scanner interface {
	Scan(dest ...any) error
}

func scanProvider(row scanner) (StoredProvider, error) {
	var p StoredProvider
	var dir, driver, name, baseURL, model, keyEnc, keyNonce string
	var active int
	err := row.Scan(&p.ID, &dir, &driver, &name, &baseURL, &model,
		&keyEnc, &keyNonce, &active, &p.CreatedAt, &p.UpdatedAt)
	if err != nil {
		return StoredProvider{}, err
	}
	p.Direction = Direction(dir)
	p.Driver = driver
	p.Name = name
	p.BaseURL = baseURL
	p.ModelHint = model
	p.APIKeyEnc = keyEnc
	p.APIKeyNonce = keyNonce
	p.IsActive = active == 1
	p.HasAPIKey = keyEnc != ""
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
