// Package agent — registry.go
//
// Catalog of agent definitions. Built-in agents are seeded on first run
// into ai_agents (is_builtin=1); admins can add custom agents and edit
// any agent's system_prompt / model / tools / limits.
package agent

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/headercat/airbrew/internal/id"
)

// Definition is the agent-level config persisted in ai_agents.
type Definition struct {
	ID           string  `json:"id"`
	Name         string  `json:"name"`
	Description  string  `json:"description"`
	Model        string  `json:"model"`
	SystemPrompt string  `json:"system_prompt"`
	Tools        []string `json:"tools"`
	Temperature  float32 `json:"temperature"`
	MaxTokens    int     `json:"max_tokens"`
	MaxTurns     int     `json:"max_turns"`
	IsBuiltin    bool    `json:"is_builtin"`
	IsActive     bool    `json:"is_active"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// Sentinel errors.
var (
	ErrNotFound     = errors.New("agent: not found")
	ErrInvalidInput = errors.New("agent: invalid input")
)

// DefinitionRepo persists ai_agents rows.
type DefinitionRepo struct {
	db *sql.DB
}

// NewDefinitionRepo returns a repo bound to db.
func NewDefinitionRepo(db *sql.DB) *DefinitionRepo { return &DefinitionRepo{db: db} }

const agentColumns = `id, name, description, model, system_prompt, tools,
temperature, max_tokens, max_turns, is_builtin, is_active, created_at, updated_at`

// List returns all agents (active first). When activeOnly is true,
// inactive rows are excluded.
func (r *DefinitionRepo) List(ctx context.Context, activeOnly bool) ([]Definition, error) {
	q := "SELECT " + agentColumns + " FROM ai_agents"
	if activeOnly {
		q += " WHERE is_active = 1"
	}
	q += " ORDER BY is_builtin DESC, is_active DESC, name ASC"
	rows, err := r.db.QueryContext(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Definition
	for rows.Next() {
		a, err := scanAgent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// Get returns one agent by ID.
func (r *DefinitionRepo) Get(ctx context.Context, id string) (Definition, error) {
	row := r.db.QueryRowContext(ctx,
		"SELECT "+agentColumns+" FROM ai_agents WHERE id = ?", id)
	a, err := scanAgent(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Definition{}, ErrNotFound
	}
	return a, err
}

// Input is the validated create/update payload.
type Input struct {
	Name         string
	Description  string
	Model        string
	SystemPrompt string
	Tools        []string
	Temperature  float32
	MaxTokens    int
	MaxTurns     int
	IsActive     bool
}

func normalize(in Input) (Input, error) {
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" {
		return in, fmt.Errorf("%w: name required", ErrInvalidInput)
	}
	if len(in.Name) > 80 {
		return in, fmt.Errorf("%w: name too long", ErrInvalidInput)
	}
	if in.Model == "" {
		return in, fmt.Errorf("%w: model required", ErrInvalidInput)
	}
	if len(in.SystemPrompt) > 8<<10 {
		return in, fmt.Errorf("%w: system_prompt too long", ErrInvalidInput)
	}
	if len(in.Tools) > 32 {
		return in, fmt.Errorf("%w: too many tools", ErrInvalidInput)
	}
	if in.MaxTurns <= 0 || in.MaxTurns > 20 {
		in.MaxTurns = 6
	}
	if in.Temperature < 0 || in.Temperature > 2 {
		in.Temperature = 0.7
	}
	if in.MaxTokens < 0 {
		in.MaxTokens = 0
	}
	return in, nil
}

// Create inserts a custom (non-builtin) agent.
func (r *DefinitionRepo) Create(ctx context.Context, in Input) (Definition, error) {
	in, err := normalize(in)
	if err != nil {
		return Definition{}, err
	}
	toolsJSON, err := json.Marshal(in.Tools)
	if err != nil {
		return Definition{}, fmt.Errorf("agent: marshal tools: %w", err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	a := Definition{
		ID: id.New(), Name: in.Name, Description: in.Description, Model: in.Model,
		SystemPrompt: in.SystemPrompt, Tools: in.Tools, Temperature: in.Temperature,
		MaxTokens: in.MaxTokens, MaxTurns: in.MaxTurns, IsActive: in.IsActive,
		IsBuiltin: false, CreatedAt: now, UpdatedAt: now,
	}
	_, err = r.db.ExecContext(ctx, `
		INSERT INTO ai_agents
		  (id, name, description, model, system_prompt, tools,
		   temperature, max_tokens, max_turns, is_builtin, is_active,
		   created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 0, ?, ?, ?)
	`, a.ID, a.Name, a.Description, a.Model, a.SystemPrompt, toolsJSON,
		a.Temperature, a.MaxTokens, a.MaxTurns, boolToInt(a.IsActive),
		a.CreatedAt, a.UpdatedAt)
	if err != nil {
		return Definition{}, fmt.Errorf("agent: insert: %w", err)
	}
	return a, nil
}

// Update modifies an existing agent. Builtin agents can be edited (the
// defaults are starting points), but their is_builtin flag cannot be
// toggled here.
func (r *DefinitionRepo) Update(ctx context.Context, id string, in Input) (Definition, error) {
	in, err := normalize(in)
	if err != nil {
		return Definition{}, err
	}
	toolsJSON, err := json.Marshal(in.Tools)
	if err != nil {
		return Definition{}, fmt.Errorf("agent: marshal tools: %w", err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	res, err := r.db.ExecContext(ctx, `
		UPDATE ai_agents SET
		  name = ?, description = ?, model = ?, system_prompt = ?, tools = ?,
		  temperature = ?, max_tokens = ?, max_turns = ?, is_active = ?,
		  updated_at = ?
		WHERE id = ?`,
		in.Name, in.Description, in.Model, in.SystemPrompt, toolsJSON,
		in.Temperature, in.MaxTokens, in.MaxTurns, boolToInt(in.IsActive),
		now, id,
	)
	if err != nil {
		return Definition{}, fmt.Errorf("agent: update: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return Definition{}, ErrNotFound
	}
	return r.Get(ctx, id)
}

// Delete removes a custom agent. Builtin agents cannot be deleted.
func (r *DefinitionRepo) Delete(ctx context.Context, id string) error {
	res, err := r.db.ExecContext(ctx,
		"DELETE FROM ai_agents WHERE id = ? AND is_builtin = 0", id)
	if err != nil {
		return fmt.Errorf("agent: delete: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// Seed inserts the built-in agent set if ai_agents is empty. Called once
// at server startup.
func (r *DefinitionRepo) Seed(ctx context.Context, defaults []Definition) error {
	var n int
	if err := r.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM ai_agents").Scan(&n); err != nil {
		return fmt.Errorf("agent: seed count: %w", err)
	}
	if n > 0 {
		return nil
	}
	now := time.Now().UTC().Truncate(time.Second)
	for _, d := range defaults {
		d.ID = id.New()
		d.IsBuiltin = true
		d.IsActive = true
		d.CreatedAt = now
		d.UpdatedAt = now
		if d.Tools == nil {
			d.Tools = []string{}
		}
		if d.MaxTurns == 0 {
			d.MaxTurns = 6
		}
		toolsJSON, _ := json.Marshal(d.Tools)
		if _, err := r.db.ExecContext(ctx, `
			INSERT INTO ai_agents
			  (id, name, description, model, system_prompt, tools,
			   temperature, max_tokens, max_turns, is_builtin, is_active,
			   created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 1, 1, ?, ?)
		`, d.ID, d.Name, d.Description, d.Model, d.SystemPrompt, toolsJSON,
			d.Temperature, d.MaxTokens, d.MaxTurns, d.CreatedAt, d.UpdatedAt,
		); err != nil {
			return fmt.Errorf("agent: seed insert %q: %w", d.Name, err)
		}
	}
	return nil
}

// DefaultBuiltins returns the seed list. The defaultModel arg lets the
// caller pick the right baseline for the active provider (e.g. gpt-4o-mini
// for OpenAI, llama3.1 for Ollama). When no provider is configured we
// still seed with a placeholder so the SPA can render.
func DefaultBuiltins(defaultModel string) []Definition {
	if defaultModel == "" {
		defaultModel = "gpt-4o-mini"
	}
	return []Definition{
		{
			Name:        "Assistant",
			Description: "General-purpose assistant. A safe default for most tasks.",
			Model:       defaultModel,
			SystemPrompt: "You are Airbrew Assistant, a helpful, concise companion. " +
				"Answer truthfully; if you do not know, say so. Prefer short replies " +
				"unless the user asks for depth.",
			Tools:       []string{"clock"},
			Temperature: 0.7,
			MaxTokens:   2048,
			MaxTurns:    6,
		},
		{
			Name:        "Analyst",
			Description: "Long-form reasoning and writing. No tools, more depth.",
			Model:       defaultModel,
			SystemPrompt: "You are Airbrew Analyst. Take time to think step by step. " +
				"Cite assumptions. Prefer structured responses with clear sections.",
			Tools:       []string{},
			Temperature: 0.4,
			MaxTokens:   4096,
			MaxTurns:    2,
		},
	}
}

// --- helpers --------------------------------------------------------------

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

type scanner interface {
	Scan(dest ...any) error
}

func scanAgent(row scanner) (Definition, error) {
	var a Definition
	var toolsJSON string
	var isBuiltin, isActive int
	if err := row.Scan(
		&a.ID, &a.Name, &a.Description, &a.Model, &a.SystemPrompt, &toolsJSON,
		&a.Temperature, &a.MaxTokens, &a.MaxTurns, &isBuiltin, &isActive,
		&a.CreatedAt, &a.UpdatedAt,
	); err != nil {
		return Definition{}, err
	}
	a.IsBuiltin = isBuiltin == 1
	a.IsActive = isActive == 1
	if toolsJSON != "" {
		_ = json.Unmarshal([]byte(toolsJSON), &a.Tools)
	}
	if a.Tools == nil {
		a.Tools = []string{}
	}
	return a, nil
}
