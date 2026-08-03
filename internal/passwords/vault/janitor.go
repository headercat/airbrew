// Package vault — janitor.go
//
// The Janitor runs two periodic cleanup passes in the background:
//
//  1. Tombstone purge: hard-deletes folders and items soft-deleted more than
//     tombstoneTTL ago, plus their archived history. Tombstones have already
//     been seen by every syncing client, so removing them does not bump the
//     per-user sync cursor (see docs/passwords.md, "Sync mechanism").
//  2. Blob orphan sweep: diffs the blob store's vault-attachments namespace
//     against the rows in vault_attachments and removes blobs that no row
//     references (e.g. a crash between Save and the DB insert, or a failed
//     blob Delete after a row was removed).
//
// Both passes are best-effort and only log; they never fail the process. The
// loop exits when ctx is cancelled.
package vault

import (
	"context"
	"log/slog"
	"time"

	"github.com/headercat/airbrew/internal/blob"
)

// tombstoneTTL is how long a soft-deleted row is kept before the janitor
// hard-deletes it. Mirrors the 30-day window documented in docs/passwords.md.
const tombstoneTTL = 30 * 24 * time.Hour

// janitorInterval is how often the cleanup loop runs.
const janitorInterval = 6 * time.Hour

// revisionTTL is how long per-item history snapshots are kept before the
// janitor starts pruning them. 365 days matches the "old password" guidance
// in the health report.
const revisionTTL = 365 * 24 * time.Hour

// revisionKeepPerItem caps the number of history snapshots per item regardless
// of age, so a frequently edited item does not grow unbounded.
const revisionKeepPerItem = 100

// attachmentNamespace is the blob namespace the orphan sweep scans. It must
// match the namespace passed to blobs.Save by the attachment upload handler.
const attachmentNamespace = "vault-attachments"

// Janitor periodically purges old tombstones and orphaned attachment blobs.
type Janitor struct {
	repo   *Repository
	blobs  blob.Store
	logger *slog.Logger
}

// NewJanitor returns a Janitor backed by repo and blobs. blobs may be nil, in
// which case the orphan sweep is skipped (the tombstone pass still runs).
func NewJanitor(repo *Repository, blobs blob.Store, logger *slog.Logger) *Janitor {
	if logger == nil {
		logger = slog.Default()
	}
	return &Janitor{repo: repo, blobs: blobs, logger: logger}
}

// Start runs the cleanup loop until ctx is cancelled. It performs one pass
// immediately on startup, then every janitorInterval. It blocks; callers are
// expected to run it in its own goroutine.
func (j *Janitor) Start(ctx context.Context) {
	run := func() {
		defer func() {
			if r := recover(); r != nil {
				j.logger.ErrorContext(ctx, "vault: janitor tick panicked", "error", r)
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
	j.purgeTombstones(ctx)
	j.purgeOldItemRevisions(ctx)
	j.sweepOrphanBlobs(ctx)
}

func (j *Janitor) purgeOldItemRevisions(ctx context.Context) {
	cutoff := time.Now().UTC().Add(-revisionTTL)
	n, err := j.repo.PurgeOldItemRevisions(ctx, cutoff, revisionKeepPerItem)
	if err != nil {
		j.logger.Warn("vault janitor: revision purge failed", "error", err)
		return
	}
	if n > 0 {
		j.logger.Info("vault janitor: pruned old item revisions", "count", n)
	}
}

func (j *Janitor) purgeTombstones(ctx context.Context) {
	cutoff := time.Now().UTC().Add(-tombstoneTTL)
	folders, items, err := j.repo.PurgeOldTombstones(ctx, cutoff)
	if err != nil {
		j.logger.Warn("vault janitor: tombstone purge failed", "error", err)
		return
	}
	if folders > 0 || items > 0 {
		j.logger.Info("vault janitor: purged tombstones",
			"folders", folders, "items", items)
	}
}

func (j *Janitor) sweepOrphanBlobs(ctx context.Context) {
	if j.blobs == nil {
		return
	}
	onDisk, err := j.blobs.List(ctx, attachmentNamespace)
	if err != nil {
		j.logger.Warn("vault janitor: blob list failed", "error", err)
		return
	}
	if len(onDisk) == 0 {
		return
	}
	referenced, err := j.repo.AllAttachmentBlobPaths(ctx)
	if err != nil {
		j.logger.Warn("vault janitor: read referenced blobs failed", "error", err)
		return
	}
	alive := make(map[string]struct{}, len(referenced))
	for _, p := range referenced {
		alive[p] = struct{}{}
	}
	var removed int
	for _, p := range onDisk {
		if _, ok := alive[p]; ok {
			continue
		}
		if err := j.blobs.Delete(ctx, p); err != nil {
			j.logger.Warn("vault janitor: orphan delete failed", "path", p, "error", err)
			continue
		}
		removed++
	}
	if removed > 0 {
		j.logger.Info("vault janitor: removed orphan blobs", "count", removed)
	}
}
