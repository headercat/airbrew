// Package outbound defines the send-side driver contract and the registry of
// concrete drivers. Each driver parses its JSON config into a typed struct and
// implements Outbounder. Active-driver resolution reads the provider table via
// internal/mail/provider and builds the matching driver on demand.
//
// Drivers are pure stdlib net/http or net/smtp clients; they never touch the
// database. The inbox service owns message persistence and calls Send.
package outbound

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/headercat/airbrew/internal/mail/letter"
	"github.com/headercat/airbrew/internal/mail/provider"
)

// Outbounder sends a single message. Implementations must be safe for
// concurrent use.
type Outbounder interface {
	Name() string
	Send(ctx context.Context, o letter.Outgoing) error
}

// Factory builds an Outbounder from a JSON config blob.
type Factory func(config json.RawMessage) (Outbounder, error)

var registry = map[string]Factory{}

// Register adds a driver factory under name. Called by each driver's init().
func Register(name string, f Factory) {
	registry[name] = f
}

// Build constructs the named driver from config. Unknown drivers return an
// error wrapping ErrUnknownDriver.
func Build(driver string, config json.RawMessage) (Outbounder, error) {
	f, ok := registry[driver]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrUnknownDriver, driver)
	}
	return f(config)
}

// ErrUnknownDriver is returned when Build is asked for an unregistered driver.
var ErrUnknownDriver = errors.New("outbound: unknown driver")

// Drivers returns the registered driver names in a stable order for listings.
func Drivers() []string {
	return []string{"sendgrid", "mailgun", "ncloud", "ses", "cloudflare", "smtp"}
}

// Resolve reads the active outbound provider and builds its driver. Returns
// (nil, nil) when no provider is active so callers can distinguish "not
// configured" from a real error.
func Resolve(ctx context.Context, repo *provider.Repository) (Outbounder, error) {
	p, err := repo.GetActive(ctx, provider.DirectionOutbound)
	if err != nil {
		if errors.Is(err, provider.ErrNoActive) {
			return nil, nil
		}
		return nil, err
	}
	return Build(p.Driver, json.RawMessage(p.Config))
}
