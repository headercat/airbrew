// Package mail wires the mail module: per-user mailboxes, message storage, the
// inbound (receive) and outbound (send) driver registries, and their HTTP
// surface.
//
// Driver layout (see docs/mail.md):
//
//	internal/mail/letter    shared MIME helpers (Address, Outgoing, BuildRFC822, Parse)
//	internal/mail/inbox     Mailbox + Message domain, repository, service
//	internal/mail/provider  admin-selectable driver registry (mail_providers)
//	internal/mail/inbound   receive drivers: cloudflare, ses (webhook) + imap, pop3 (poll)
//	internal/mail/outbound  send drivers: sendgrid, mailgun, ncloud, ses, cloudflare, smtp
//	internal/mail/handler   JSON HTTP handlers
//
// An administrator activates exactly one inbound and one outbound driver via
// the admin endpoints; the active driver is resolved at runtime. The Module
// starts an inbound Coordinator with the process-lifetime context that runs
// the matching poll driver (imap/pop3) when one is active.
package mail

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/headercat/airbrew/internal/audit"
	"github.com/headercat/airbrew/internal/blob"
	"github.com/headercat/airbrew/internal/db"
	"github.com/headercat/airbrew/internal/mail/handler"
	"github.com/headercat/airbrew/internal/mail/inbound"
	"github.com/headercat/airbrew/internal/mail/inbox"
	"github.com/headercat/airbrew/internal/mail/provider"
	"github.com/headercat/airbrew/internal/modules"
)

// Module bundles the mail services and HTTP handlers.
type Module struct {
	state   *modules.State
	audit   *audit.Service
	inbox   *inbox.Service
	prov    *provider.Repository
	user    *handler.Handler
	admin   *handler.AdminHandler
	webhook *handler.WebhookHandler
	coord   *inbound.Coordinator
}

// New builds the mail Module bound to the given database.
func New(database *db.DB, state *modules.State, auditSvc *audit.Service, blobs blob.Store) *Module {
	repo := inbox.NewRepository(database.DB)
	provRepo := provider.NewRepository(database.DB)
	in := inbox.NewService(repo, blobs)
	coord := inbound.NewCoordinator(provRepo, &ingestAdapter{svc: in}, nil)
	return &Module{
		state:   state,
		audit:   auditSvc,
		inbox:   in,
		prov:    provRepo,
		user:    handler.New(in, provRepo, blobs),
		admin:   handler.NewAdmin(provRepo),
		webhook: handler.NewWebhook(provRepo, in),
		coord:   coord,
	}
}

// Status is the public GET /api/mail/status handler.
func (m *Module) Status(w http.ResponseWriter, r *http.Request) {
	enabled := true
	if m.state != nil {
		if v, err := m.state.IsEnabled(r.Context(), "mail"); err == nil {
			enabled = v
		}
	}
	status := "ok"
	if !enabled {
		status = "disabled"
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"module":           "mail",
		"status":           status,
		"enabled":          enabled,
		"inbound_drivers":  inbound.Drivers(),
		"outbound_drivers": outboundDrivers(),
	})
}

// RegisterPublicRoutes mounts routes reachable without a session: the status
// endpoint and the inbound webhook receivers.
func (m *Module) RegisterPublicRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/mail/status", m.Status)
	m.webhook.RegisterRoutes(mux)
}

// RegisterRoutes mounts the authenticated user endpoints on mux. The caller is
// expected to wrap mux with the session middleware.
func (m *Module) RegisterRoutes(mux *http.ServeMux) {
	m.user.RegisterRoutes(mux)
}

// RegisterAdminRoutes mounts the admin provider-config endpoints on mux. The
// caller is expected to mount mux within the admin (RequireAdmin) tree.
func (m *Module) RegisterAdminRoutes(mux *http.ServeMux) {
	m.admin.RegisterRoutes(mux)
}

// Start launches the inbound poll coordinator with the given lifecycle
// context. It returns immediately; the coordinator runs in a goroutine that
// exits when ctx is cancelled.
func (m *Module) Start(ctx context.Context) {
	go m.coord.Run(ctx)
}

// ingestAdapter adapts inbox.Service.Ingest to the inbound.Ingester contract.
type ingestAdapter struct{ svc *inbox.Service }

func (a *ingestAdapter) Ingest(ctx context.Context, recipient string, raw []byte, receivedAt time.Time) error {
	_, err := a.svc.Ingest(ctx, recipient, raw, receivedAt)
	return err
}

// outboundDrivers returns the send driver names without importing the outbound
// package here (the handler already imports it; this keeps the status endpoint
// self-contained). Listed inline to avoid an extra dependency edge.
func outboundDrivers() []string {
	return []string{"sendgrid", "mailgun", "ncloud", "ses", "cloudflare", "smtp"}
}
