package files

import (
	"context"
	"log/slog"
	"time"

	"github.com/headercat/airbrew/internal/blob"
)

// janitorInterval is how often the background loop runs.
const janitorInterval = 6 * time.Hour

// Janitor purges expired shares and sweeps orphaned drive blobs.
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
				j.logger.ErrorContext(ctx, "drive: janitor tick panicked", "error", r)
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
	j.deactivateExpiredShares(ctx)
	j.sweepOrphanBlobs(ctx)
}

func (j *Janitor) deactivateExpiredShares(ctx context.Context) {
	n, err := j.repo.PurgeExpiredShares(ctx, time.Now())
	if err != nil {
		j.logger.WarnContext(ctx, "drive: deactivate expired shares failed", "error", err)
		return
	}
	if n > 0 {
		j.logger.InfoContext(ctx, "drive: deactivated expired shares", "count", n)
	}
}

func (j *Janitor) sweepOrphanBlobs(ctx context.Context) {
	if j.blobs == nil {
		return
	}
	listed, err := j.blobs.List(ctx, Namespace)
	if err != nil {
		j.logger.WarnContext(ctx, "drive: list blobs failed", "error", err)
		return
	}
	if len(listed) == 0 {
		return
	}
	known, err := j.repo.AllBlobPathsAll(ctx)
	if err != nil {
		j.logger.WarnContext(ctx, "drive: load known blob paths failed", "error", err)
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
			j.logger.WarnContext(ctx, "drive: delete orphan blob failed", "path", p, "error", err)
			continue
		}
		removed++
	}
	if removed > 0 {
		j.logger.InfoContext(ctx, "drive: swept orphan blobs", "count", removed)
	}
}
