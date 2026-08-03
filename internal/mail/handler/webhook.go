package handler

import (
	"crypto/subtle"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/headercat/airbrew/internal/mail/inbound"
	"github.com/headercat/airbrew/internal/mail/inbox"
	"github.com/headercat/airbrew/internal/mail/provider"
)

// WebhookHandler exposes the public inbound webhook receivers for the push
// drivers (cloudflare, ses). Each request is authenticated by a bearer secret
// stored in the active provider's config.
type WebhookHandler struct {
	repo  *provider.Repository
	inbox *inbox.Service
}

// NewWebhook builds a WebhookHandler.
func NewWebhook(repo *provider.Repository, in *inbox.Service) *WebhookHandler {
	return &WebhookHandler{repo: repo, inbox: in}
}

// RegisterRoutes mounts the webhook receivers on mux.
func (h *WebhookHandler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/mail/inbound/{driver}", h.receive)
}

func (h *WebhookHandler) receive(w http.ResponseWriter, r *http.Request) {
	driver := r.PathValue("driver")
	p, err := h.repo.GetActive(r.Context(), provider.DirectionInbound)
	if err != nil {
		status := http.StatusServiceUnavailable
		if errors.Is(err, provider.ErrNoActive) {
			status = http.StatusNotFound
		}
		respondErr(w, status, "no_provider", "inbound delivery not configured")
		return
	}
	if p.Driver != driver {
		respondErr(w, http.StatusConflict, "driver_mismatch", "active inbound driver is "+p.Driver)
		return
	}
	want := inbound.PushDriverSecret(p.Driver, []byte(p.Config))
	got := bearerToken(r)
	if want == "" || subtle.ConstantTimeCompare([]byte(got), []byte(want)) != 1 {
		respondErr(w, http.StatusUnauthorized, "unauthorized", "invalid webhook secret")
		return
	}
	recipient := strings.TrimSpace(r.Header.Get("X-Airbrew-Mailbox"))
	if recipient == "" {
		recipient = r.URL.Query().Get("mailbox")
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, 25<<20+1)) // 25 MiB cap
	if err != nil {
		respondErr(w, http.StatusBadRequest, "invalid_request", "could not read body")
		return
	}
	if int64(len(raw)) > 25<<20 {
		// Oversized payload. Without this check, LimitReader silently
		// truncates a 30 MiB message to exactly 25 MiB and letter.Parse
		// stores a partial MIME tree.
		respondErr(w, http.StatusRequestEntityTooLarge, "payload_too_large", "message exceeds 25 MiB cap")
		return
	}
	msg, err := h.inbox.Ingest(r.Context(), recipient, raw, time.Now().UTC())
	if err != nil {
		if errors.Is(err, inbox.ErrDuplicate) {
			jsonResp(w, http.StatusOK, map[string]string{"status": "duplicate"})
			return
		}
		if errors.Is(err, inbox.ErrMailboxNotFound) {
			respondErr(w, http.StatusNotFound, "mailbox_not_found", "no mailbox for recipient")
			return
		}
		writeErr(w, err)
		return
	}
	jsonResp(w, http.StatusCreated, map[string]string{"id": msg.ID, "mailbox": msg.MailboxID})
}

func bearerToken(r *http.Request) string {
	v := r.Header.Get("Authorization")
	if v == "" {
		return ""
	}
	if strings.HasPrefix(strings.ToLower(v), "bearer ") {
		return strings.TrimSpace(v[7:])
	}
	return strings.TrimSpace(v)
}
