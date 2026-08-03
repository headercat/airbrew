package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/headercat/airbrew/internal/audit"
	"github.com/headercat/airbrew/internal/mail/inbound"
	"github.com/headercat/airbrew/internal/mail/letter"
	"github.com/headercat/airbrew/internal/mail/outbound"
	"github.com/headercat/airbrew/internal/mail/provider"
)

// AdminHandler exposes the admin-only provider config endpoints. The caller is
// expected to mount it under a mux already wrapped with RequireAdmin.
type AdminHandler struct {
	repo  *provider.Repository
	audit *audit.Service
}

// NewAdmin builds an AdminHandler. auditSvc may be nil; when present, provider
// changes are recorded in the audit log.
func NewAdmin(repo *provider.Repository, auditSvc *audit.Service) *AdminHandler {
	return &AdminHandler{repo: repo, audit: auditSvc}
}

// RegisterRoutes mounts the admin endpoints on mux.
func (a *AdminHandler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/admin/mail/providers", a.list)
	mux.HandleFunc("PUT /api/admin/mail/providers", a.upsert)
	mux.HandleFunc("DELETE /api/admin/mail/providers/{id}", a.delete)
	mux.HandleFunc("POST /api/admin/mail/providers/{id}/test", a.test)
}

type providerResp struct {
	ID        string `json:"id"`
	Direction string `json:"direction"`
	Driver    string `json:"driver"`
	Config    any    `json:"config"`
	IsActive  bool   `json:"is_active"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

func toProviderResp(p provider.Provider) providerResp {
	var cfg any
	_ = json.Unmarshal([]byte(p.Config), &cfg)
	if cfg == nil {
		cfg = map[string]any{}
	}
	return providerResp{
		ID: p.ID, Direction: string(p.Direction), Driver: p.Driver,
		Config: cfg, IsActive: p.IsActive,
		CreatedAt: p.CreatedAt.UTC().Format(timeRFC3339),
		UpdatedAt: p.UpdatedAt.UTC().Format(timeRFC3339),
	}
}

func (a *AdminHandler) list(w http.ResponseWriter, r *http.Request) {
	provs, err := a.repo.List(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	catalog := map[string]any{
		"inbound":  inbound.Drivers(),
		"outbound": outbound.Drivers(),
	}
	out := make([]providerResp, 0, len(provs))
	for _, p := range provs {
		out = append(out, toProviderResp(p))
	}
	jsonResp(w, http.StatusOK, map[string]any{"providers": out, "drivers": catalog})
}

type upsertReq struct {
	Direction string          `json:"direction"`
	Driver    string          `json:"driver"`
	Config    json.RawMessage `json:"config"`
}

func (a *AdminHandler) upsert(w http.ResponseWriter, r *http.Request) {
	var req upsertReq
	if err := decodeJSON(r, &req); err != nil {
		respondErr(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	p, err := a.repo.UpsertAndActivate(r.Context(), provider.Direction(req.Direction), req.Driver, string(req.Config))
	if err != nil {
		writeErr(w, err)
		return
	}
	if a.audit != nil {
		a.audit.Log(r.Context(), audit.Entry{
			EventType:  "mail.provider_upserted",
			TargetType: "mail_provider", TargetID: p.ID,
			IPAddress: clientIP(r),
			UserAgent: r.UserAgent(),
			Metadata:  map[string]any{"direction": p.Direction, "driver": p.Driver},
		})
	}
	jsonResp(w, http.StatusCreated, toProviderResp(p))
}

func (a *AdminHandler) delete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := a.repo.Delete(r.Context(), id); err != nil {
		writeErr(w, err)
		return
	}
	if a.audit != nil {
		a.audit.Log(r.Context(), audit.Entry{
			EventType:  "mail.provider_deleted",
			TargetType: "mail_provider", TargetID: id,
			IPAddress: clientIP(r),
			UserAgent: r.UserAgent(),
		})
	}
	jsonResp(w, http.StatusOK, map[string]bool{"ok": true})
}

// test builds the outbound/inbound driver from an existing provider config
// and exercises it. For outbound drivers it sends a small diagnostic test
// message to the operator-supplied "test_to" address (read from the request
// body or the provider config). For inbound poll drivers it runs a single Poll
// against a discard ingester so connectivity errors surface. Webhook inbound
// drivers have no live dependency to probe, so they report a shape-only OK.
func (a *AdminHandler) test(w http.ResponseWriter, r *http.Request) {
	p, err := a.repo.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		writeErr(w, err)
		return
	}
	var req struct {
		TestTo string `json:"test_to"`
	}
	if r.ContentLength != 0 {
		_ = decodeJSON(r, &req)
	}
	switch p.Direction {
	case provider.DirectionOutbound:
		drv, err := outbound.Build(p.Driver, []byte(p.Config))
		if err != nil {
			writeErr(w, err)
			return
		}
		recipient := strings.TrimSpace(req.TestTo)
		if recipient == "" {
			recipient = testRecipientFromConfig([]byte(p.Config))
		}
		if recipient == "" {
			jsonResp(w, http.StatusOK, map[string]any{
				"ok":     true,
				"driver": drv.Name(),
				"note":   "config parses; supply test_to to send a probe",
			})
			return
		}
		to, err := letter.ParseAddressList(recipient)
		if err != nil || len(to) == 0 {
			respondErr(w, http.StatusBadRequest, "invalid_request",
				"test_to must be a valid email address")
			return
		}
		from := testSenderFromConfig([]byte(p.Config))
		if from == "" {
			from = "airbrew-test@localhost"
		}
		out := letter.Outgoing{
			From: letter.Address{Address: from, Name: "Airbrew Test"},
			To:   to,
			Subject: "Airbrew mail provider test",
			Text: "This is an automated connectivity test from your Airbrew " +
				"mail provider configuration. If you received it, the " +
				drv.Name() + " outbound driver is working.",
		}
		if err := drv.Send(r.Context(), out); err != nil {
			respondErr(w, http.StatusBadGateway, "send_failed", err.Error())
			return
		}
		jsonResp(w, http.StatusOK, map[string]any{
			"ok":     true,
			"driver": drv.Name(),
			"sent_to": recipient,
		})
	case provider.DirectionInbound:
		if !inbound.IsPollDriver(p.Driver) {
			jsonResp(w, http.StatusOK, map[string]any{
				"ok": true, "driver": p.Driver, "webhook": true,
			})
			return
		}
		poller, err := inbound.Build(p.Driver, []byte(p.Config))
		if err != nil {
			writeErr(w, err)
			return
		}
		if err := poller.Poll(r.Context(), &discardIngester{}); err != nil {
			respondErr(w, http.StatusBadGateway, "poll_failed", err.Error())
			return
		}
		jsonResp(w, http.StatusOK, map[string]any{
			"ok": true, "driver": p.Driver,
		})
	default:
		respondErr(w, http.StatusBadRequest, "invalid_request", "unknown direction")
	}
}

// discardIngester accepts messages but does nothing, used for admin
// connectivity probes so a poll driver's login/fetch path is exercised without
// storing junk mail.
type discardIngester struct{}

func (*discardIngester) Ingest(context.Context, string, []byte, time.Time) error { return nil }

// testRecipientFromConfig pulls a fallback recipient out of known driver
// config shapes (smtp.from / cloudflare.worker_url owner is unreachable, but
// some drivers carry a sensible probe target). Returns "" when none.
func testRecipientFromConfig(raw json.RawMessage) string {
	var probe struct {
		TestTo  string `json:"test_to"`
		From    string `json:"from"`
		Sender  string `json:"sender_address"`
		Address string `json:"address"`
	}
	_ = json.Unmarshal(raw, &probe)
	for _, v := range []string{probe.TestTo, probe.From, probe.Sender, probe.Address} {
		if at := strings.IndexByte(v, '@'); at > 0 && at < len(v)-1 {
			return v
		}
	}
	return ""
}

// testSenderFromConfig picks an envelope sender that the driver is known to
// accept, falling back to the empty default the driver will replace.
func testSenderFromConfig(raw json.RawMessage) string {
	var probe struct {
		From    string `json:"from"`
		Sender  string `json:"sender_address"`
		Address string `json:"address"`
		APIKey  string `json:"api_key"`
	}
	_ = json.Unmarshal(raw, &probe)
	for _, v := range []string{probe.From, probe.Sender, probe.Address} {
		if at := strings.IndexByte(v, '@'); at > 0 && at < len(v)-1 {
			return v
		}
	}
	return ""
}
