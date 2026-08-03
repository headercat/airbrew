package contact

import (
	"context"
	"log/slog"
	"time"

	"github.com/headercat/airbrew/internal/blob"
)

// janitorInterval is how often the background loop runs.
const janitorInterval = 6 * time.Hour

// Janitor sweeps orphaned contact avatar blobs: blobs that exist in the store
// under the contacts namespace but are no longer referenced by any contact row
// (the result of crashes, failed uploads, or concurrent avatar replacements).
type Janitor struct {
	repo   *Repository
	blobs  blob.Store
	logger *slog.Logger
}

// NewJanitor builds a Janitor. blobs and logger may be nil (nil blobs skips the
// orphan sweep; nil logger falls back to slog.Default()).
func NewJanitor(repo *Repository, blobs blob.Store, logger *slog.Logger) *Janitor {
	if logger == nil {
		logger = slog.Default()
	}
	return &Janitor{repo: repo, blobs: blobs, logger: logger}
}

// Start runs the loop until ctx is cancelled. It does one pass immediately,
// then ticks every janitorInterval. Best-effort: errors only log at Warn.
func (j *Janitor) Start(ctx context.Context) {
	run := func() {
		defer func() {
			if r := recover(); r != nil {
				j.logger.ErrorContext(ctx, "contacts: janitor tick panicked", "error", r)
			}
		}()
		j.runOnce(ctx)
	}
	run()
	t := time.NewTicker(janitorInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			run()
		}
	}
}

func (j *Janitor) runOnce(ctx context.Context) {
	if j.blobs == nil {
		return
	}
	listed, err := j.blobs.List(ctx, Namespace)
	if err != nil {
		j.logger.WarnContext(ctx, "contacts: list avatar blobs failed", "error", err)
		return
	}
	if len(listed) == 0 {
		return
	}
	known, err := j.repo.AllAvatarPathsAll(ctx)
	if err != nil {
		j.logger.WarnContext(ctx, "contacts: load known avatar paths failed", "error", err)
		return
	}
	knownSet := make(map[string]struct{}, len(known))
	for _, p := range known {
		knownSet[p] = struct{}{}
	}
	var removed int
	for _, p := range listed {
		select {
		case <-ctx.Done():
			return
		default:
		}
		if _, ok := knownSet[p]; ok {
			continue
		}
		if err := j.blobs.Delete(ctx, p); err != nil {
			j.logger.WarnContext(ctx, "contacts: delete orphan avatar failed", "path", p, "error", err)
			continue
		}
		removed++
	}
	if removed > 0 {
		j.logger.InfoContext(ctx, "contacts: swept orphan avatar blobs", "count", removed)
	}
}
