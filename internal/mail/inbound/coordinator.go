package inbound

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/headercat/airbrew/internal/mail/provider"
)

// Coordinator watches the active inbound provider and runs the matching poll
// loop. Push drivers (cloudflare, ses) need no background loop, so when the
// active provider is one of those — or none at all — the coordinator stops any
// running poller. It refreshes periodically so an admin provider change takes
// effect without a restart.
type Coordinator struct {
	repo    *provider.Repository
	ingest  Ingester
	log     *slog.Logger
	refresh time.Duration

	mu      sync.Mutex
	current string // active poll driver name, "" when idle
	config  string // active poll driver config, to detect changes
	cancel  context.CancelFunc
}

// NewCoordinator builds a Coordinator bound to the provider registry.
func NewCoordinator(repo *provider.Repository, ingest Ingester, log *slog.Logger) *Coordinator {
	if log == nil {
		log = slog.Default()
	}
	return &Coordinator{repo: repo, ingest: ingest, log: log, refresh: 30 * time.Second}
}

// Run blocks until ctx is cancelled, periodically syncing the active poller.
func (c *Coordinator) Run(ctx context.Context) {
	c.log.Info("mail: inbound coordinator started", "refresh", c.refresh)
	c.sync(ctx)
	t := time.NewTicker(c.refresh)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			c.stop()
			return
		case <-t.C:
			c.sync(ctx)
		}
	}
}

func (c *Coordinator) sync(ctx context.Context) {
	p, err := c.repo.GetActive(ctx, provider.DirectionInbound)
	if err != nil {
		if !errors.Is(err, provider.ErrNoActive) {
			c.log.Warn("mail: read active inbound provider", "error", err)
		}
		c.stop()
		return
	}
	if !IsPollDriver(p.Driver) {
		c.stop()
		return
	}
	c.mu.Lock()
	running := c.current == p.Driver && c.config == p.Config
	c.mu.Unlock()
	if running {
		return
	}
	c.stop()
	poller, err := Build(p.Driver, []byte(p.Config))
	if err != nil {
		c.log.Error("mail: build inbound poller", "driver", p.Driver, "error", err)
		return
	}
	pollCtx, cancel := context.WithCancel(ctx)
	c.mu.Lock()
	c.current = p.Driver
	c.config = p.Config
	c.cancel = cancel
	c.mu.Unlock()
	c.log.Info("mail: starting inbound poller", "driver", p.Driver, "interval", poller.Interval())
	go c.loop(pollCtx, poller)
}

func (c *Coordinator) loop(ctx context.Context, p Poller) {
	base := p.Interval()
	var failures int
	for {
		if ctx.Err() != nil {
			return
		}
		err := p.Poll(ctx, c.ingest)
		wait := base
		if err != nil {
			c.log.Warn("mail: inbound poll", "driver", p.Name(), "error", err)
			failures++
			// Exponential backoff capped at 10x the base interval so a
			// misconfigured server or temporary network outage does not
			// hammer the remote on every tick. The cap keeps the loop
			// responsive once the provider comes back.
			backoff := time.Duration(1<<min(failures, 6)) * base
			if backoff > 10*base {
				backoff = 10 * base
			}
			wait = backoff
		} else {
			failures = 0
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}
}

func (c *Coordinator) stop() {
	c.mu.Lock()
	cancel := c.cancel
	c.cancel = nil
	c.current = ""
	c.config = ""
	c.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}
