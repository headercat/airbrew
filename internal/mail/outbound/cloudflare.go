package outbound

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/headercat/airbrew/internal/mail/letter"
)

func init() { Register("cloudflare", buildCloudflare) }

// CloudflareConfig posts outgoing mail to an operator-deployed Worker, which
// typically forwards to MailChannels (the standard Cloudflare outbound path).
// The Worker URL and a shared bearer secret are supplied by the admin.
type CloudflareConfig struct {
	WorkerURL string `json:"worker_url"` // e.g. https://send.example.workers.dev
	Secret    string `json:"secret"`     // bearer token the Worker checks
}

type cloudflareDriver struct {
	cfg    CloudflareConfig
	client *http.Client
}

func buildCloudflare(raw json.RawMessage) (Outbounder, error) {
	var cfg CloudflareConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, fmt.Errorf("cloudflare: bad config: %w", err)
	}
	if cfg.WorkerURL == "" {
		return nil, fmt.Errorf("cloudflare: worker_url required")
	}
	return &cloudflareDriver{cfg: cfg, client: &http.Client{Timeout: 30 * time.Second}}, nil
}

func (d *cloudflareDriver) Name() string { return "cloudflare" }

func (d *cloudflareDriver) Send(ctx context.Context, o letter.Outgoing) error {
	raw, err := letter.BuildRFC822(o)
	if err != nil {
		return err
	}
	body, err := json.Marshal(map[string]any{
		"from":    o.From.Address,
		"to":      o.Recipients(),
		"raw":     b64(raw),
		"subject": o.Subject,
	})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.cfg.WorkerURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if d.cfg.Secret != "" {
		req.Header.Set("Authorization", "Bearer "+d.cfg.Secret)
	}
	resp, err := d.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("cloudflare: %s: %s", resp.Status, readBodySnippet(resp))
	}
	return nil
}
