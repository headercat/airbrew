// Package inbound defines the receive-side driver contract for mail.
//
// Receive drivers come in two shapes:
//
//   - Push drivers (cloudflare, ses): an external provider POSTs the raw message
//     to a public webhook. Those handlers live in internal/mail/handler and
//     authenticate with the provider's configured secret.
//   - Poll drivers (imap, pop3): a Coordinator goroutine, started by the Module
//     with the process-lifetime context, periodically connects and ingests new
//     mail. Only poll drivers register here.
//
// Both shapes funnel into an Ingester (backed by inbox.Service) that parses the
// raw RFC822 and stores it under the right mailbox.
package inbound

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// RawMessage is a received email before it is parsed and stored.
type RawMessage struct {
	Recipient  string
	From       string
	To         []string
	Raw        []byte
	ReceivedAt time.Time
}

// Ingester accepts a raw message for the given recipient mailbox address.
type Ingester interface {
	Ingest(ctx context.Context, recipient string, raw []byte, receivedAt time.Time) error
}

// Poller connects to a remote server and ingests new messages until ctx is
// done or the sweep completes. The Coordinator calls Poll repeatedly, waiting
// Interval() between calls.
type Poller interface {
	Name() string
	Poll(ctx context.Context, ingest Ingester) error
	Interval() time.Duration
}

// Factory builds a Poller from JSON config.
type Factory func(config json.RawMessage) (Poller, error)

var registry = map[string]Factory{}

// Register adds a poll driver under name. Called by each driver's init().
func Register(name string, f Factory) { registry[name] = f }

// ErrUnknownDriver is returned when Build is asked for an unregistered driver.
var ErrUnknownDriver = errors.New("inbound: unknown driver")

// Build constructs the named poll driver from config.
func Build(driver string, config json.RawMessage) (Poller, error) {
	f, ok := registry[driver]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrUnknownDriver, driver)
	}
	return f(config)
}

// Drivers returns all inbound driver names (push + poll) for listings.
func Drivers() []string { return []string{"cloudflare", "ses", "imap", "pop3"} }

// IsPollDriver reports whether driver runs as a background poller (vs. webhook).
func IsPollDriver(driver string) bool {
	switch driver {
	case "imap", "pop3":
		return true
	}
	return false
}

// PushDriverSecret reads the bearer secret from a push driver's config JSON.
// It returns "" when the key is absent.
func PushDriverSecret(driver string, config json.RawMessage) string {
	var cfg struct {
		Secret string `json:"secret"`
	}
	_ = json.Unmarshal(config, &cfg)
	return cfg.Secret
}
