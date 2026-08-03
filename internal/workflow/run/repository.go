package run

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/headercat/airbrew/internal/id"
	"github.com/headercat/airbrew/internal/workflow/defn"
)

// newID is internal/id.New wrapped so repository methods whose parameter
// is named "id" do not shadow the package import.
func newID() string { return id.New() }

// Repository persists workflows, versions, runs and step runs.
type Repository struct {
	db *sql.DB
}

// NewRepository returns a Repository bound to db.
func NewRepository(db *sql.DB) *Repository { return &Repository{db: db} }

const workflowColumns = `id, user_id, name, COALESCE(description,''), definition,
	version, is_active, COALESCE(trigger_type,''), COALESCE(webhook_token,''),
	COALESCE(cron_expr,''), created_at, updated_at`

// CreateWorkflow inserts a new workflow at version 1. The caller is expected
// to have validated the definition; trigger columns are derived here so the
// save path is the single source of truth.
func (r *Repository) CreateWorkflow(ctx context.Context, w *Workflow) error {
	now := time.Now().UTC().Truncate(time.Second)
	w.Version = 1
	w.CreatedAt = now
	w.UpdatedAt = now
	applyTriggerColumns(w)
	def, err := w.Definition.Marshal()
	if err != nil {
		return err
	}
	w.ID = id.New()
	if _, err := r.db.ExecContext(ctx, `
		INSERT INTO workflows
		  (id, user_id, name, description, definition, version, is_active,
		   trigger_type, webhook_token, cron_expr, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		w.ID, w.UserID, w.Name, w.Description, def, w.Version,
		boolToInt(w.IsActive), nullable(w.TriggerType), nullable(w.WebhookToken),
		nullable(w.CronExpr), now, now,
	); err != nil {
		if isUniqueViolation(err) {
			return ErrTokenTaken
		}
		return fmt.Errorf("workflow: insert workflow: %w", err)
	}
	return r.snapshotVersion(ctx, w)
}

// GetWorkflow returns one workflow owned by userID.
func (r *Repository) GetWorkflow(ctx context.Context, userID, id string) (*Workflow, error) {
	row := r.db.QueryRowContext(ctx,
		"SELECT "+workflowColumns+" FROM workflows WHERE id = ? AND user_id = ?", id, userID)
	w, err := scanWorkflow(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return w, err
}

// GetWorkflowByWebhookToken returns the active workflow listening on the
// given webhook token. Used by the webhook receiver hot path; no user scope
// because the token is unguessable.
func (r *Repository) GetWorkflowByWebhookToken(ctx context.Context, token string) (*Workflow, error) {
	row := r.db.QueryRowContext(ctx,
		"SELECT "+workflowColumns+" FROM workflows WHERE webhook_token = ? AND is_active = 1",
		token,
	)
	w, err := scanWorkflow(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return w, err
}

// ListActiveByTrigger returns every active workflow of the given trigger
// type. Used by the scheduler to enumerate cron schedules at startup.
func (r *Repository) ListActiveByTrigger(ctx context.Context, t TriggerType) ([]*Workflow, error) {
	q := "SELECT " + workflowColumns +
		" FROM workflows WHERE is_active = 1 AND trigger_type = ? ORDER BY created_at ASC"
	rows, err := r.db.QueryContext(ctx, q, string(t))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Workflow
	for rows.Next() {
		w, err := scanWorkflow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

// GetLastFiredAt returns the persisted last dispatch timestamp for a schedule
// workflow (or the zero time when never fired). Used by the scheduler to avoid
// re-firing a cron schedule after a process restart within the same minute.
func (r *Repository) GetLastFiredAt(ctx context.Context, id string) (time.Time, error) {
	var t sql.NullTime
	err := r.db.QueryRowContext(ctx,
		`SELECT last_fired_at FROM workflows WHERE id = ?`, id).Scan(&t)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return time.Time{}, nil
		}
		return time.Time{}, err
	}
	if !t.Valid {
		return time.Time{}, nil
	}
	return t.Time.UTC(), nil
}

// MarkScheduleFired stamps last_fired_at for id. The scheduler calls this
// immediately after dispatching a run so the next tick (in this process or a
// restarted one) can see the schedule already ran this minute.
func (r *Repository) MarkScheduleFired(ctx context.Context, id string, at time.Time) error {
	_, err := r.db.ExecContext(ctx,
		`UPDATE workflows SET last_fired_at = ? WHERE id = ?`,
		at.UTC().Truncate(time.Second), id)
	return err
}

// ListWorkflows returns every workflow owned by userID, newest first.
func (r *Repository) ListWorkflows(ctx context.Context, userID string, limit, offset int) ([]*Workflow, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	rows, err := r.db.QueryContext(ctx,
		"SELECT "+workflowColumns+
			" FROM workflows WHERE user_id = ? ORDER BY created_at DESC LIMIT ? OFFSET ?",
		userID, limit, offset,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Workflow
	for rows.Next() {
		w, err := scanWorkflow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

// UpdateWorkflow applies an update to a workflow. version is bumped if and
// only if the definition changed. The previous definition is snapshotted
// before the update so history is contiguous.
func (r *Repository) UpdateWorkflow(ctx context.Context, userID, id string, patch PatchWorkflowInput) (*Workflow, error) {
	cur, err := r.GetWorkflow(ctx, userID, id)
	if err != nil {
		return nil, err
	}
	if patch.Name != nil {
		cur.Name = strings.TrimSpace(*patch.Name)
		if cur.Name == "" {
			return nil, fmt.Errorf("%w: name is required", ErrInvalidInput)
		}
	}
	if patch.Description != nil {
		cur.Description = *patch.Description
	}
	defChanged := false
	if patch.Definition != nil {
		if err := patch.Definition.Validate(); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrDefinitionInvalid, err)
		}
		if err := ValidateScheduleConfig(*patch.Definition); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrDefinitionInvalid, err)
		}
		// Only bump the version if the serialised form actually changed so
		// cosmetic save round-trips don't pollute history.
		newDef, mErr := patch.Definition.Marshal()
		if mErr != nil {
			return nil, mErr
		}
		oldDef, _ := cur.Definition.Marshal()
		if newDef != oldDef {
			// Snapshot the *old* definition at its current version before
			// we overwrite it, so workflow_versions is contiguous.
			if err := r.snapshotVersion(ctx, cur); err != nil {
				return nil, err
			}
			cur.Definition = *patch.Definition
			cur.Version++
			defChanged = true
		}
	}
	if defChanged {
		applyTriggerColumns(cur)
	}
	cur.UpdatedAt = time.Now().UTC().Truncate(time.Second)
	def, err := cur.Definition.Marshal()
	if err != nil {
		return nil, err
	}
	if _, err := r.db.ExecContext(ctx, `
		UPDATE workflows SET name = ?, description = ?, definition = ?, version = ?,
		  is_active = ?, trigger_type = ?, webhook_token = ?, cron_expr = ?, updated_at = ?
		WHERE id = ? AND user_id = ?
	`,
		cur.Name, cur.Description, def, cur.Version,
		boolToInt(cur.IsActive), nullable(cur.TriggerType), nullable(cur.WebhookToken),
		nullable(cur.CronExpr), cur.UpdatedAt, id, userID,
	); err != nil {
		if isUniqueViolation(err) {
			return nil, ErrTokenTaken
		}
		return nil, fmt.Errorf("workflow: update workflow: %w", err)
	}
	return cur, nil
}

// SetActive toggles a workflow's activation. On activate, the webhook token
// is (re)minted for webhook triggers so revoking + re-enabling issues a new
// URL. Returns the refreshed workflow.
func (r *Repository) SetActive(ctx context.Context, userID, id string, active bool) (*Workflow, error) {
	w, err := r.GetWorkflow(ctx, userID, id)
	if err != nil {
		return nil, err
	}
	if active == w.IsActive {
		return w, nil
	}
	if active {
		if err := w.Definition.Validate(); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrDefinitionInvalid, err)
		}
		if err := ValidateScheduleConfig(w.Definition); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrDefinitionInvalid, err)
		}
		// (Re)mint webhook token on activation so revoking truly revokes.
		if defn.TriggerTypeOf(w.Definition.TriggerNode().Type) == TriggerWebhook {
			w.WebhookToken = newID()
		}
	} else {
		w.WebhookToken = ""
	}
	w.IsActive = active
	applyTriggerColumns(w)
	w.UpdatedAt = time.Now().UTC().Truncate(time.Second)
	if _, err := r.db.ExecContext(ctx, `
		UPDATE workflows SET is_active = ?, trigger_type = ?, webhook_token = ?,
		  cron_expr = ?, updated_at = ?
		WHERE id = ? AND user_id = ?
	`,
		boolToInt(w.IsActive), nullable(w.TriggerType), nullable(w.WebhookToken),
		nullable(w.CronExpr), w.UpdatedAt, id, userID,
	); err != nil {
		if isUniqueViolation(err) {
			return nil, ErrTokenTaken
		}
		return nil, fmt.Errorf("workflow: set active: %w", err)
	}
	return w, nil
}

// DeleteWorkflow removes a workflow, cascading to versions + runs. The
// caller is expected to deactivate first.
func (r *Repository) DeleteWorkflow(ctx context.Context, userID, id string) error {
	res, err := r.db.ExecContext(ctx,
		"DELETE FROM workflows WHERE id = ? AND user_id = ?", id, userID)
	if err != nil {
		return fmt.Errorf("workflow: delete workflow: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// snapshotVersion inserts a workflow_versions row for w at w.Version. It
// dedupes on (workflow_id, version) so the snapshot is idempotent — calling
// it on a freshly created workflow then again before an update does not
// create two rows at version 1.
func (r *Repository) snapshotVersion(ctx context.Context, w *Workflow) error {
	def, err := w.Definition.Marshal()
	if err != nil {
		return err
	}
	_, err = r.db.ExecContext(ctx, `
		INSERT OR IGNORE INTO workflow_versions (id, workflow_id, version, definition)
		VALUES (?, ?, ?, ?)
	`, id.New(), w.ID, w.Version, def)
	if err != nil {
		return fmt.Errorf("workflow: snapshot version: %w", err)
	}
	return nil
}

// ListVersions returns the version history for a workflow, newest first.
func (r *Repository) ListVersions(ctx context.Context, userID, workflowID string, limit, offset int) ([]*Version, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	// Scope by user_id via a join so a user cannot read another user's
	// history by guessing the workflow id.
	rows, err := r.db.QueryContext(ctx, `
		SELECT v.id, v.workflow_id, v.version, v.definition, v.created_at
		FROM workflow_versions v
		JOIN workflows w ON w.id = v.workflow_id
		WHERE v.workflow_id = ? AND w.user_id = ?
		ORDER BY v.version DESC
		LIMIT ? OFFSET ?
	`, workflowID, userID, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Version
	for rows.Next() {
		var v Version
		var def string
		if err := rows.Scan(&v.ID, &v.WorkflowID, &v.Version, &def, &v.CreatedAt); err != nil {
			return nil, err
		}
		d, perr := defn.UnmarshalDefinition(def)
		if perr != nil {
			return nil, perr
		}
		v.Definition = d
		out = append(out, &v)
	}
	return out, rows.Err()
}

// --- runs ------------------------------------------------------------------

// CreateRun inserts a run row at the given status (usually Running).
func (r *Repository) CreateRun(ctx context.Context, run *Run) error {
	run.ID = id.New()
	run.StartedAt = time.Now().UTC().Truncate(time.Second)
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO workflow_runs
		  (id, workflow_id, user_id, version, status, trigger, input_json, error, started_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		run.ID, run.WorkflowID, run.UserID, run.Version,
		string(run.Status), string(run.Trigger), capJSON(run.InputJSON), run.Error, run.StartedAt,
	)
	if err != nil {
		return fmt.Errorf("workflow: insert run: %w", err)
	}
	return nil
}

// FinishRun sets the run's terminal state, error text, and finished_at.
func (r *Repository) FinishRun(ctx context.Context, id string, status RunStatus, errMsg string) error {
	now := time.Now().UTC().Truncate(time.Second)
	_, err := r.db.ExecContext(ctx, `
		UPDATE workflow_runs SET status = ?, error = ?, finished_at = ?
		WHERE id = ?
	`, string(status), errMsg, now, id)
	if err != nil {
		return fmt.Errorf("workflow: finish run: %w", err)
	}
	return nil
}

// GetRun returns one run, scoped to userID.
func (r *Repository) GetRun(ctx context.Context, userID, id string) (*Run, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT id, workflow_id, user_id, version, status, trigger,
		       COALESCE(input_json,'{}'), COALESCE(error,''),
		       started_at, finished_at
		FROM workflow_runs WHERE id = ? AND user_id = ?
	`, id, userID)
	var run Run
	var status, trigger string
	var finished sql.NullTime
	if err := row.Scan(
		&run.ID, &run.WorkflowID, &run.UserID, &run.Version, &status, &trigger,
		&run.InputJSON, &run.Error, &run.StartedAt, &finished,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	run.Status = RunStatus(status)
	run.Trigger = RunTrigger(trigger)
	if finished.Valid {
		t := finished.Time.UTC()
		run.FinishedAt = &t
	}
	return &run, nil
}

// GetRunByID returns one run without user scoping. It is used by internal
// execution paths that already operate on a trusted persisted run id.
func (r *Repository) GetRunByID(ctx context.Context, id string) (*Run, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT id, workflow_id, user_id, version, status, trigger,
		       COALESCE(input_json,'{}'), COALESCE(error,''),
		       started_at, finished_at
		FROM workflow_runs WHERE id = ?
	`, id)
	return scanRun(row)
}

// ListRunsFilter controls which runs are returned.
type ListRunsFilter struct {
	UserID     string
	WorkflowID string
	Status     RunStatus
	Limit      int
	Offset     int
}

// ListRuns returns runs matching the filter, newest first.
func (r *Repository) ListRuns(ctx context.Context, f ListRunsFilter) ([]*Run, error) {
	if f.Limit <= 0 || f.Limit > 200 {
		f.Limit = 50
	}
	if f.Offset < 0 {
		f.Offset = 0
	}
	q := `
		SELECT id, workflow_id, user_id, version, status, trigger,
		       COALESCE(input_json,'{}'), COALESCE(error,''),
		       started_at, finished_at
		FROM workflow_runs WHERE user_id = ?`
	args := []any{f.UserID}
	if f.WorkflowID != "" {
		q += " AND workflow_id = ?"
		args = append(args, f.WorkflowID)
	}
	if f.Status != "" {
		q += " AND status = ?"
		args = append(args, string(f.Status))
	}
	q += " ORDER BY started_at DESC LIMIT ? OFFSET ?"
	args = append(args, f.Limit, f.Offset)
	rows, err := r.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Run
	for rows.Next() {
		run, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, run)
	}
	return out, rows.Err()
}

// --- steps -----------------------------------------------------------------

// CreateStep inserts a step row. seq is set from the engine's visit order.
func (r *Repository) CreateStep(ctx context.Context, s *StepRun) error {
	s.ID = id.New()
	s.StartedAt = time.Now().UTC().Truncate(time.Second)
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO workflow_step_runs
		  (id, run_id, node_id, node_type, status, input_json, output_json,
		   error, duration_ms, seq, started_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		s.ID, s.RunID, s.NodeID, s.NodeType, string(s.Status),
		capJSON(s.InputJSON), capJSON(s.OutputJSON), s.Error, s.DurationMs, s.Seq, s.StartedAt,
	)
	if err != nil {
		return fmt.Errorf("workflow: insert step: %w", err)
	}
	return nil
}

// FinishStep sets a step's terminal state and timing.
func (r *Repository) FinishStep(ctx context.Context, id string, status StepStatus, output, errMsg string, durationMs int64) error {
	now := time.Now().UTC().Truncate(time.Second)
	_, err := r.db.ExecContext(ctx, `
		UPDATE workflow_step_runs SET status = ?, output_json = ?, error = ?,
		  duration_ms = ?, finished_at = ?
		WHERE id = ?
	`, string(status), capJSON(output), errMsg, durationMs, now, id)
	if err != nil {
		return fmt.Errorf("workflow: finish step: %w", err)
	}
	return nil
}

// ListSteps returns every step in a run in visit order. Scoped to userID via
// a join so cross-user reads are impossible.
func (r *Repository) ListSteps(ctx context.Context, userID, runID string) ([]*StepRun, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT s.id, s.run_id, s.node_id, s.node_type, s.status,
		       COALESCE(s.input_json,'{}'), COALESCE(s.output_json,'{}'),
		       COALESCE(s.error,''), s.duration_ms, s.seq, s.started_at, s.finished_at
		FROM workflow_step_runs s
		JOIN workflow_runs r ON r.id = s.run_id
		WHERE s.run_id = ? AND r.user_id = ?
		ORDER BY s.seq ASC
	`, runID, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*StepRun
	for rows.Next() {
		var s StepRun
		var status string
		var finished sql.NullTime
		if err := rows.Scan(
			&s.ID, &s.RunID, &s.NodeID, &s.NodeType, &status,
			&s.InputJSON, &s.OutputJSON, &s.Error, &s.DurationMs, &s.Seq,
			&s.StartedAt, &finished,
		); err != nil {
			return nil, err
		}
		s.Status = StepStatus(status)
		if finished.Valid {
			t := finished.Time.UTC()
			s.FinishedAt = &t
		}
		out = append(out, &s)
	}
	return out, rows.Err()
}

// --- helpers ---------------------------------------------------------------

type scanner interface {
	Scan(dest ...any) error
}

func scanRun(row scanner) (*Run, error) {
	var run Run
	var status, trigger string
	var finished sql.NullTime
	if err := row.Scan(
		&run.ID, &run.WorkflowID, &run.UserID, &run.Version, &status, &trigger,
		&run.InputJSON, &run.Error, &run.StartedAt, &finished,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	run.Status = RunStatus(status)
	run.Trigger = RunTrigger(trigger)
	if finished.Valid {
		t := finished.Time.UTC()
		run.FinishedAt = &t
	}
	return &run, nil
}

func scanWorkflow(row scanner) (*Workflow, error) {
	var w Workflow
	var isActive int
	var def, triggerType, webhookToken, cronExpr string
	if err := row.Scan(
		&w.ID, &w.UserID, &w.Name, &w.Description, &def,
		&w.Version, &isActive, &triggerType, &webhookToken, &cronExpr,
		&w.CreatedAt, &w.UpdatedAt,
	); err != nil {
		return nil, err
	}
	w.IsActive = isActive == 1
	w.TriggerType = triggerType
	w.WebhookToken = webhookToken
	w.CronExpr = cronExpr
	d, err := defn.UnmarshalDefinition(def)
	if err != nil {
		return nil, err
	}
	w.Definition = d
	return &w, nil
}

// applyTriggerColumns denormalises the trigger type + per-type columns from
// the definition so the hot path (webhook receiver, scheduler) does not have
// to parse JSON.
func applyTriggerColumns(w *Workflow) {
	w.TriggerType = ""
	w.WebhookToken = ""
	w.CronExpr = ""
	if !w.IsActive {
		return
	}
	t := w.Definition.TriggerNode()
	if t == nil {
		return
	}
	switch defn.TriggerTypeOf(t.Type) {
	case TriggerManual:
		w.TriggerType = string(TriggerManual)
	case TriggerWebhook:
		w.TriggerType = string(TriggerWebhook)
		if w.WebhookToken == "" {
			w.WebhookToken = id.New()
		}
	case TriggerSchedule:
		w.TriggerType = string(TriggerSchedule)
		w.CronExpr = scheduleCron(t)
	}
}

// scheduleCron extracts the cron expression from a schedule trigger's
// config. Empty / malformed config yields "" (the validator rejects it).
func scheduleCron(n *defn.Node) string {
	if len(n.Config) == 0 {
		return ""
	}
	var cfg struct {
		Cron string `json:"cron"`
	}
	if err := json.Unmarshal(n.Config, &cfg); err != nil {
		return ""
	}
	return strings.TrimSpace(cfg.Cron)
}

// capJSON truncates a JSON string to a sane upper bound so a runaway HTTP
// response cannot blow up the step_run rows.
const maxJSONLen = 64 * 1024

func capJSON(s string) string {
	if len(s) <= maxJSONLen {
		return s
	}
	b, _ := json.Marshal(map[string]any{
		"_truncated": true,
		"preview":    s[:maxJSONLen],
	})
	return string(b)
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// isUniqueViolation reports whether err is a SQLite UNIQUE/PK constraint
// failure (modernc.org/sqlite prefixes constraint errors with "constraint").
func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "constraint failed") ||
		strings.Contains(msg, "UNIQUE constraint failed")
}
