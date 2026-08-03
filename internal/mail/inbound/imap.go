package inbound

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/emersion/go-imap"
	"github.com/emersion/go-imap/client"
	"github.com/headercat/airbrew/internal/mail/inbox"
)

func init() { Register("imap", buildIMAP) }

// IMAPConfig connects to an IMAP server and delivers newly-seen messages into
// the given airbrew mailbox address. Seen UIDs are tracked in memory, so a
// restart re-fetches the mailbox once.
type IMAPConfig struct {
	Host        string `json:"host"`
	Port        int    `json:"port"`
	Username    string `json:"username"`
	Password    string `json:"password"`
	TLS         string `json:"tls"`     // "", "starttls", "tls" (default tls)
	Mailbox     string `json:"mailbox"` // remote folder, default INBOX
	Address     string `json:"address"` // destination airbrew mailbox address
	IntervalSec int    `json:"interval_sec"`
}

type imapPoller struct {
	cfg  IMAPConfig
	seen map[uint32]struct{}
	mu   sync.Mutex
	log  *slog.Logger
}

func buildIMAP(raw json.RawMessage) (Poller, error) {
	var cfg IMAPConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, fmt.Errorf("imap: bad config: %w", err)
	}
	if cfg.Host == "" || cfg.Port == 0 || cfg.Address == "" {
		return nil, fmt.Errorf("imap: host, port and address required")
	}
	if cfg.Mailbox == "" {
		cfg.Mailbox = "INBOX"
	}
	if cfg.TLS == "" {
		cfg.TLS = "tls"
	}
	return &imapPoller{cfg: cfg, seen: map[uint32]struct{}{}, log: slog.Default()}, nil
}

func (p *imapPoller) Name() string { return "imap" }

func (p *imapPoller) Interval() time.Duration {
	if p.cfg.IntervalSec > 0 {
		return time.Duration(p.cfg.IntervalSec) * time.Second
	}
	return 60 * time.Second
}

func (p *imapPoller) Poll(ctx context.Context, ingest Ingester) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	addr := net.JoinHostPort(p.cfg.Host, fmt.Sprintf("%d", p.cfg.Port))
	tlsCfg := &tls.Config{ServerName: p.cfg.Host}

	var c *client.Client
	var err error
	switch strings.ToLower(p.cfg.TLS) {
	case "tls", "":
		c, err = client.DialTLS(addr, tlsCfg)
	case "starttls":
		c, err = client.Dial(addr)
	default:
		c, err = client.Dial(addr)
	}
	if err != nil {
		return fmt.Errorf("imap: dial: %w", err)
	}
	defer c.Logout()

	if strings.EqualFold(p.cfg.TLS, "starttls") {
		if err := c.StartTLS(tlsCfg); err != nil {
			return fmt.Errorf("imap: starttls: %w", err)
		}
	}
	if err := c.Login(p.cfg.Username, p.cfg.Password); err != nil {
		return fmt.Errorf("imap: login: %w", err)
	}
	mbox, err := c.Select(p.cfg.Mailbox, true) // read-only
	if err != nil {
		return fmt.Errorf("imap: select: %w", err)
	}
	if mbox.Messages == 0 {
		return nil
	}

	criteria := imap.NewSearchCriteria()
	criteria.WithoutFlags = []string{imap.SeenFlag}
	// Search unseen first; fall back to all if the server lacks search.
	uids, err := c.UidSearch(criteria)
	if err != nil {
		return fmt.Errorf("imap: search: %w", err)
	}
	var toFetch []uint32
	p.mu.Lock()
	for _, uid := range uids {
		if _, ok := p.seen[uid]; !ok {
			toFetch = append(toFetch, uid)
		}
	}
	p.mu.Unlock()
	if len(toFetch) == 0 {
		return nil
	}

	set := new(imap.SeqSet)
	for _, uid := range toFetch {
		set.AddNum(uid)
	}
	section := &imap.BodySectionName{Peek: true}
	items := []imap.FetchItem{imap.FetchUid, imap.FetchInternalDate, imap.FetchEnvelope, section.FetchItem()}

	messages := make(chan *imap.Message, 10)
	done := make(chan error, 1)
	go func() { done <- c.UidFetch(set, items, messages) }()

	for msg := range messages {
		if ctx.Err() != nil {
			// The fetch goroutine may still be writing to the messages
			// channel; if we just `break` and wait on `done` we deadlock
			// once the channel fills. Force the fetcher to return by
			// tearing the connection down, then drain whatever it had
			// buffered so the goroutine exits cleanly (and the deferred
			// Logout does not double-close).
			_ = c.Logout()
			for range messages {
			}
			<-done
			return ctx.Err()
		}
		r := msg.GetBody(section)
		if r == nil {
			continue
		}
		raw, err := io.ReadAll(io.LimitReader(r, maxMessageBytes+1))
		if err != nil {
			// Mark seen so we do not re-download the same body on every
			// poll cycle forever (matches the POP3 retr-failure path).
			p.log.Warn("imap: read body failed, skipping on future polls", "uid", msg.Uid, "error", err)
			p.mu.Lock()
			p.seen[msg.Uid] = struct{}{}
			p.mu.Unlock()
			continue
		}
		if int64(len(raw)) > maxMessageBytes {
			p.log.Warn("imap: message exceeds size cap, skipping future polls", "uid", msg.Uid, "cap", maxMessageBytes)
			p.mu.Lock()
			p.seen[msg.Uid] = struct{}{}
			p.mu.Unlock()
			continue
		}
		date := msg.InternalDate
		if date.IsZero() {
			date = time.Now().UTC()
		}
		if err := ingest.Ingest(ctx, p.cfg.Address, raw, date); err != nil {
			// ErrProbe is the admin connectivity-test sentinel: stop the
			// sweep without marking the UID seen (IMAP uses BODY.PEEK and
			// never deletes upstream, so a probe is otherwise harmless).
			if errors.Is(err, inbox.ErrProbe) {
				return nil
			}
			// ErrDuplicate means we already stored the message in a previous
			// cycle. Treat it as success so the UID is marked seen and we do
			// not re-fetch it on every tick after a coordinator restart.
			if !errors.Is(err, inbox.ErrDuplicate) {
				p.log.Warn("imap: ingest failed", "uid", msg.Uid, "error", err)
				continue
			}
		}
		p.mu.Lock()
		p.seen[msg.Uid] = struct{}{}
		p.mu.Unlock()
	}
	if err := <-done; err != nil {
		return fmt.Errorf("imap: fetch: %w", err)
	}
	return nil
}
