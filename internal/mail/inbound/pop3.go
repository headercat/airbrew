package inbound

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"sync"
	"time"
)

func init() { Register("pop3", buildPOP3) }

// POP3Config connects to a POP3 server and delivers fetched mail into the given
// airbrew mailbox address.
type POP3Config struct {
	Host        string `json:"host"`
	Port        int    `json:"port"`
	Username    string `json:"username"`
	Password    string `json:"password"`
	TLS         string `json:"tls"`     // "", "starttls", "tls"
	Address     string `json:"address"` // destination mailbox address in airbrew
	Delete      bool   `json:"delete_after_fetch"`
	IntervalSec int    `json:"interval_sec"`
}

type pop3Poller struct {
	cfg  POP3Config
	seen map[string]struct{}
	mu   sync.Mutex
	log  *slog.Logger
}

func buildPOP3(raw json.RawMessage) (Poller, error) {
	var cfg POP3Config
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, fmt.Errorf("pop3: bad config: %w", err)
	}
	if cfg.Host == "" || cfg.Port == 0 || cfg.Address == "" {
		return nil, fmt.Errorf("pop3: host, port and address required")
	}
	return &pop3Poller{cfg: cfg, seen: map[string]struct{}{}, log: slog.Default()}, nil
}

func (p *pop3Poller) Name() string { return "pop3" }

func (p *pop3Poller) Interval() time.Duration {
	if p.cfg.IntervalSec > 0 {
		return time.Duration(p.cfg.IntervalSec) * time.Second
	}
	return 60 * time.Second
}

func (p *pop3Poller) Poll(ctx context.Context, ingest Ingester) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	addr := net.JoinHostPort(p.cfg.Host, fmt.Sprintf("%d", p.cfg.Port))
	c, err := dialPOP3(addr, p.cfg.Host, strings.EqualFold(p.cfg.TLS, "tls"))
	if err != nil {
		return fmt.Errorf("pop3: dial: %w", err)
	}
	defer c.Quit()

	if strings.EqualFold(p.cfg.TLS, "starttls") {
		if err := c.Stls(p.cfg.Host); err != nil {
			return fmt.Errorf("pop3: stls: %w", err)
		}
	}
	if err := c.User(p.cfg.Username); err != nil {
		return fmt.Errorf("pop3: user: %w", err)
	}
	if err := c.Pass(p.cfg.Password); err != nil {
		return fmt.Errorf("pop3: pass: %w", err)
	}
	uids, err := c.uidl()
	if err != nil {
		return fmt.Errorf("pop3: uidl: %w", err)
	}
	for n, uid := range uids {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		p.mu.Lock()
		_, seen := p.seen[uid]
		p.mu.Unlock()
		if seen {
			continue
		}
		raw, err := c.retr(n)
		if err != nil {
			p.log.Warn("pop3: retr failed", "uid", uid, "error", err)
			continue
		}
		if err := ingest.Ingest(ctx, p.cfg.Address, raw, time.Now().UTC()); err != nil {
			p.log.Warn("pop3: ingest failed", "uid", uid, "error", err)
			continue
		}
		p.mu.Lock()
		p.seen[uid] = struct{}{}
		p.mu.Unlock()
		if p.cfg.Delete {
			if err := c.dele(n); err != nil {
				p.log.Warn("pop3: dele failed", "uid", uid, "error", err)
			}
		}
	}
	return nil
}
