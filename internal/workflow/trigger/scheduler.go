// Package trigger contains workflow trigger dispatchers.
package trigger

import (
	"context"
	"log/slog"
	"sync"
	"time"

	wfexec "github.com/headercat/airbrew/internal/workflow/exec"
	"github.com/headercat/airbrew/internal/workflow/run"
)

// Scheduler scans active schedule workflows and dispatches matching runs.
type Scheduler struct {
	repo   *run.Repository
	engine *wfexec.Engine
	log    *slog.Logger
	mu     sync.Mutex
	seen   map[string]string
}

// NewScheduler returns a schedule trigger dispatcher.
func NewScheduler(repo *run.Repository, engine *wfexec.Engine, log *slog.Logger) *Scheduler {
	if log == nil {
		log = slog.Default()
	}
	return &Scheduler{repo: repo, engine: engine, log: log, seen: map[string]string{}}
}

// Start runs until ctx is cancelled.
func (s *Scheduler) Start(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			s.tick(ctx, now.UTC().Truncate(time.Minute))
		}
	}
}

func (s *Scheduler) tick(ctx context.Context, minute time.Time) {
	items, err := s.repo.ListActiveByTrigger(ctx, run.TriggerSchedule)
	if err != nil {
		s.log.WarnContext(ctx, "workflow scheduler: list active schedules failed", "error", err)
		return
	}
	for _, item := range items {
		ok, err := run.CronMatches(item.CronExpr, minute)
		if err != nil {
			s.log.WarnContext(ctx, "workflow scheduler: invalid cron skipped", "workflow_id", item.ID, "error", err)
			continue
		}
		if !ok || s.alreadyDispatched(item.ID, minute) {
			continue
		}
		wf := item
		go func() {
			_, err := s.engine.Execute(context.Background(), wfexec.Request{
				Workflow: wf,
				Trigger:  run.RunBySchedule,
				Input: map[string]any{
					"scheduled_at": minute.Format(time.RFC3339),
					"cron":         wf.CronExpr,
				},
			})
			if err != nil {
				s.log.Warn("workflow scheduler: run failed", "workflow_id", wf.ID, "error", err)
			}
		}()
	}
}

func (s *Scheduler) alreadyDispatched(workflowID string, minute time.Time) bool {
	key := minute.Format("2006-01-02T15:04Z")
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.seen[workflowID] == key {
		return true
	}
	s.seen[workflowID] = key
	if len(s.seen) > 1000 {
		s.seen = map[string]string{workflowID: key}
	}
	return false
}
