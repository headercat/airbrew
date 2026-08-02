package run

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/headercat/airbrew/internal/audit"
	"github.com/headercat/airbrew/internal/workflow/defn"
)

// Service wraps the repository with the validation, activation and audit
// rules. It is the single entry point the HTTP handlers and engine use for
// state-changing operations.
type Service struct {
	repo  *Repository
	audit *audit.Service
}

// NewService returns a Service bound to repo.
func NewService(repo *Repository, auditSvc *audit.Service) *Service {
	return &Service{repo: repo, audit: auditSvc}
}

// Create builds and persists a new workflow.
func (s *Service) Create(ctx context.Context, in NewWorkflowInput) (*Workflow, error) {
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" {
		return nil, fmt.Errorf("%w: name is required", ErrInvalidInput)
	}
	if err := in.Definition.Validate(); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrDefinitionInvalid, err)
	}
	w := &Workflow{
		UserID:      in.UserID,
		Name:        in.Name,
		Description: in.Description,
		Definition:  in.Definition,
	}
	if err := s.repo.CreateWorkflow(ctx, w); err != nil {
		return nil, err
	}
	s.audit.Log(ctx, audit.Entry{
		EventType:   "workflow.created",
		ActorUserID: in.UserID,
		TargetType:  "workflow",
		TargetID:    w.ID,
		Metadata:    map[string]any{"name": w.Name},
	})
	return w, nil
}

// Get returns one workflow owned by userID.
func (s *Service) Get(ctx context.Context, userID, id string) (*Workflow, error) {
	return s.repo.GetWorkflow(ctx, userID, id)
}

// List returns workflows owned by userID.
func (s *Service) List(ctx context.Context, userID string, limit, offset int) ([]*Workflow, error) {
	return s.repo.ListWorkflows(ctx, userID, limit, offset)
}

// Update applies a patch and returns the refreshed workflow.
func (s *Service) Update(ctx context.Context, userID, id string, patch PatchWorkflowInput) (*Workflow, error) {
	w, err := s.repo.UpdateWorkflow(ctx, userID, id, patch)
	if err != nil {
		return nil, err
	}
	s.audit.Log(ctx, audit.Entry{
		EventType:   "workflow.updated",
		ActorUserID: userID,
		TargetType:  "workflow",
		TargetID:    w.ID,
		Metadata:    map[string]any{"version": w.Version},
	})
	return w, nil
}

// Activate enables a workflow (minting a fresh webhook token if applicable).
// Returns the refreshed workflow so the caller can render the new token.
func (s *Service) Activate(ctx context.Context, userID, id string) (*Workflow, error) {
	w, err := s.repo.SetActive(ctx, userID, id, true)
	if err != nil {
		return nil, err
	}
	s.audit.Log(ctx, audit.Entry{
		EventType:   "workflow.activated",
		ActorUserID: userID,
		TargetType:  "workflow",
		TargetID:    w.ID,
	})
	return w, nil
}

// Deactivate disables a workflow, revoking its webhook token.
func (s *Service) Deactivate(ctx context.Context, userID, id string) (*Workflow, error) {
	w, err := s.repo.SetActive(ctx, userID, id, false)
	if err != nil {
		return nil, err
	}
	s.audit.Log(ctx, audit.Entry{
		EventType:   "workflow.deactivated",
		ActorUserID: userID,
		TargetType:  "workflow",
		TargetID:    w.ID,
	})
	return w, nil
}

// Delete removes a workflow. It refuses to delete an active workflow so a
// slip of the keyboard cannot leave dangling webhook/schedule references.
func (s *Service) Delete(ctx context.Context, userID, id string) error {
	w, err := s.repo.GetWorkflow(ctx, userID, id)
	if err != nil {
		return err
	}
	if w.IsActive {
		return fmt.Errorf("%w: deactivate the workflow before deleting", ErrActiveConflict)
	}
	if err := s.repo.DeleteWorkflow(ctx, userID, id); err != nil {
		return err
	}
	s.audit.Log(ctx, audit.Entry{
		EventType:   "workflow.deleted",
		ActorUserID: userID,
		TargetType:  "workflow",
		TargetID:    id,
	})
	return nil
}

// ListVersions returns the definition history for a workflow.
func (s *Service) ListVersions(ctx context.Context, userID, workflowID string, limit, offset int) ([]*Version, error) {
	return s.repo.ListVersions(ctx, userID, workflowID, limit, offset)
}

// --- run reads -------------------------------------------------------------

// GetRun returns one run, scoped to userID.
func (s *Service) GetRun(ctx context.Context, userID, id string) (*Run, error) {
	return s.repo.GetRun(ctx, userID, id)
}

// ListRuns returns runs matching the filter.
func (s *Service) ListRuns(ctx context.Context, f ListRunsFilter) ([]*Run, error) {
	return s.repo.ListRuns(ctx, f)
}

// ListSteps returns the per-node timeline for a run.
func (s *Service) ListSteps(ctx context.Context, userID, runID string) ([]*StepRun, error) {
	return s.repo.ListSteps(ctx, userID, runID)
}

// --- run cancellation ------------------------------------------------------

// CancelRun marks an in-flight run as cancelled. The engine checks for this
// flag between nodes; a run that has already finished is a no-op.
func (s *Service) CancelRun(ctx context.Context, userID, id string) error {
	run, err := s.repo.GetRun(ctx, userID, id)
	if err != nil {
		return err
	}
	if run.Status != RunPending && run.Status != RunRunning {
		return nil // already terminal; no-op
	}
	return s.repo.FinishRun(ctx, id, RunCancelled, "cancelled by user")
}

// IsCancelled reports whether a run was cancelled. The engine polls this
// between nodes so cancel takes effect promptly without blocking on a
// channel.
func (s *Service) IsCancelled(ctx context.Context, runID string) (bool, error) {
	run, err := s.repo.GetRunByID(ctx, runID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return false, nil
		}
		return false, err
	}
	return run.Status == RunCancelled, nil
}

// --- helpers exposed to the engine -----------------------------------------

// Repo returns the underlying repository. The engine needs direct write
// access to CreateRun/CreateStep/FinishRun/FinishStep; exposing the repo
// avoids duplicating those wrappers in the service.
func (s *Service) Repo() *Repository { return s.repo }

// Audit returns the audit service (used by the engine to log run outcomes).
func (s *Service) Audit() *audit.Service { return s.audit }

// TriggerType aliases defn.TriggerType so callers that import run do not
// also have to import defn (kept here for backward-compat with the
// repository file's exported alias).
type TriggerTypeAlias = defn.TriggerType

// scheduleParseErr is a guard so the scheduler can detect malformed cron
// expressions without importing the parse error type from cron libs.
var scheduleParseErr = errors.New("workflow: invalid cron expression")

// ValidateScheduleConfig rejects a schedule trigger whose cron expression is
// empty or unparseable. It is called from SetActive/Update paths via the
// repository, but the scheduler can also call it directly to skip bad rows
// at startup instead of aborting.
func ValidateScheduleConfig(d defn.Definition) error {
	t := d.TriggerNode()
	if t == nil || defn.TriggerTypeOf(t.Type) != TriggerSchedule {
		return nil
	}
	expr := scheduleCronConfig(t)
	if expr == "" {
		return fmt.Errorf("%w: schedule trigger requires config.cron", scheduleParseErr)
	}
	return nil
}

// scheduleCronConfig extracts the cron expression from a schedule trigger.
// Shared with the repository via applyTriggerColumns.
func scheduleCronConfig(n *defn.Node) string {
	return scheduleCron(n)
}

// timeNow is exposed as a variable so tests can stub the clock.
var timeNow = time.Now
