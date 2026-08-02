// Package run holds the workflow run domain: the Workflow entity (the user's
// saved graph + activation state), its persisted Definition snapshots
// (versions), and the run + per-step execution records the engine writes
// while walking the graph.
//
// The repository talks to SQLite; the service wraps it with the validation,
// activation, and version-bump rules so the HTTP handlers and the engine
// stay thin.
package run

import (
	"errors"
	"time"

	"github.com/headercat/airbrew/internal/workflow/defn"
)

// Sentinel errors. The HTTP layer maps these to status codes.
var (
	// ErrNotFound is returned when a workflow, run, or step is missing.
	ErrNotFound = errors.New("workflow: not found")
	// ErrInvalidInput is returned on shape-validation failure.
	ErrInvalidInput = errors.New("workflow: invalid input")
	// ErrDefinitionInvalid wraps a defn.ValidationError so the handler can
	// distinguish bad JSON shapes from bad request payloads.
	ErrDefinitionInvalid = errors.New("workflow: definition invalid")
	// ErrActiveConflict is returned when an action is blocked by the current
	// activation state (e.g. deleting an active workflow).
	ErrActiveConflict = errors.New("workflow: active conflict")
	// ErrTokenTaken is returned when a webhook token collision occurs.
	// Astronomically unlikely with internal/id, but surfaced for retries.
	ErrTokenTaken = errors.New("workflow: webhook token already taken")
)

// TriggerType aliases defn.TriggerType so callers that import run do not
// also have to import defn.
type TriggerType = defn.TriggerType

// Re-exported trigger kinds for callers that don't want to type the string.
const (
	TriggerManual   = defn.TriggerManual
	TriggerWebhook  = defn.TriggerWebhook
	TriggerSchedule = defn.TriggerSchedule
)

// Workflow is the persisted automation graph + activation state.
type Workflow struct {
	ID           string
	UserID       string
	Name         string
	Description  string
	Definition   defn.Definition
	Version      int
	IsActive     bool
	TriggerType  string // "" when inactive or no trigger
	WebhookToken string // set when TriggerType == webhook
	CronExpr     string // set when TriggerType == schedule
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// Version is an append-only snapshot of a workflow's definition.
type Version struct {
	ID         string
	WorkflowID string
	Version    int
	Definition defn.Definition
	CreatedAt  time.Time
}

// RunStatus enumerates the lifecycle states of a run.
type RunStatus string

const (
	RunPending   RunStatus = "pending"
	RunRunning   RunStatus = "running"
	RunSuccess   RunStatus = "success"
	RunFailed    RunStatus = "failed"
	RunCancelled RunStatus = "cancelled"
	RunTimedOut  RunStatus = "timed_out"
)

// RunTrigger enumerates what started a run.
type RunTrigger string

const (
	RunByManual   RunTrigger = "manual"
	RunByWebhook  RunTrigger = "webhook"
	RunBySchedule RunTrigger = "schedule"
)

// Run is one execution of a workflow.
type Run struct {
	ID         string
	WorkflowID string
	UserID     string
	Version    int
	Status     RunStatus
	Trigger    RunTrigger
	InputJSON  string
	Error      string
	StartedAt  time.Time
	FinishedAt *time.Time
}

// StepStatus enumerates the lifecycle states of a single node execution.
type StepStatus string

const (
	StepPending  StepStatus = "pending"
	StepRunning  StepStatus = "running"
	StepSuccess  StepStatus = "success"
	StepFailed   StepStatus = "failed"
	StepSkipped  StepStatus = "skipped"
)

// StepRun is one visited node inside a Run.
type StepRun struct {
	ID         string
	RunID      string
	NodeID     string
	NodeType   string
	Status     StepStatus
	InputJSON  string
	OutputJSON string
	Error      string
	DurationMs int64
	Seq        int
	StartedAt  time.Time
	FinishedAt *time.Time
}

// NewWorkflowInput carries the editable fields for creating a workflow.
type NewWorkflowInput struct {
	UserID      string
	Name        string
	Description string
	Definition  defn.Definition
}

// PatchWorkflowInput carries the editable fields for updating a workflow.
// nil pointers mean "leave unchanged".
type PatchWorkflowInput struct {
	Name        *string
	Description *string
	Definition  *defn.Definition
}
