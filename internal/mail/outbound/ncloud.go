package outbound

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/headercat/airbrew/internal/mail/letter"
)

func init() { Register("ncloud", buildNcloud) }

// NcloudConfig authenticates with the Ncloud SENS email API.
type NcloudConfig struct {
	AccessKey string `json:"access_key"` // x-ncp-iam-access-key
	SecretKey string `json:"secret_key"` // HMAC secret
	BaseURL   string `json:"base_url"`   // default https://mail.api.ncloud.com
	Path      string `json:"path"`       // default /v1/mail/orders
}

type ncloudDriver struct {
	cfg    NcloudConfig
	base   string
	client *http.Client
}

func buildNcloud(raw json.RawMessage) (Outbounder, error) {
	var cfg NcloudConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, fmt.Errorf("ncloud: bad config: %w", err)
	}
	if cfg.AccessKey == "" || cfg.SecretKey == "" {
		return nil, fmt.Errorf("ncloud: access_key and secret_key required")
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = "https://mail.api.ncloud.com"
	}
	if cfg.Path == "" {
		cfg.Path = "/v1/mail/orders"
	}
	return &ncloudDriver{cfg: cfg, base: cfg.BaseURL, client: &http.Client{Timeout: 30 * time.Second}}, nil
}

func (d *ncloudDriver) Name() string { return "ncloud" }

func (d *ncloudDriver) Send(ctx context.Context, o letter.Outgoing) error {
	// Ncloud SENS does not accept attachments on the same JSON endpoint
	// (it requires a separate file-upload flow this driver does not
	// implement). Reject up-front instead of silently shipping a message
	// that is missing its attachments.
	if len(o.Attachments) > 0 {
		return errors.New("ncloud: attachments are not supported by this driver")
	}
	recipients := make([]map[string]string, 0, len(o.To)+len(o.Cc))
	for _, a := range o.To {
		recipients = append(recipients, map[string]string{"address": a.Address, "name": a.Name, "type": "R"})
	}
	for _, a := range o.Cc {
		recipients = append(recipients, map[string]string{"address": a.Address, "name": a.Name, "type": "C"})
	}
	for _, a := range o.Bcc {
		recipients = append(recipients, map[string]string{"address": a.Address, "name": a.Name, "type": "B"})
	}
	body := map[string]any{
		"senderAddress":  o.From.Address,
		"senderName":     o.From.Name,
		"title":          o.Subject,
		"body":           ifEmpty(o.HTML, o.Text),
		"recipients":     recipients,
		"confirmAndSend": false,
	}
	if o.HTML != "" {
		body["isHtml"] = true
	}
	buf, err := json.Marshal(body)
	if err != nil {
		return err
	}
	endpoint, err := url.JoinPath(d.cfg.BaseURL, d.cfg.Path)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(buf))
	if err != nil {
		return err
	}
	ts := time.Now().UnixMilli()
	sig := ncloudSign(http.MethodPost, d.cfg.Path, ts, d.cfg.AccessKey, d.cfg.SecretKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-ncp-apigw-timestamp", fmt.Sprintf("%d", ts))
	req.Header.Set("x-ncp-iam-access-key", d.cfg.AccessKey)
	req.Header.Set("x-ncp-apigw-signature-v2", sig)
	resp, err := d.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("ncloud: %s: %s", resp.Status, readBodySnippet(resp))
	}
	return nil
}

// ncloudSign computes the Message HMAC-SHA256 used by the x-ncp-apigw-signature-v2
// header: HMAC(secret, method + " " + path + "\n" + timestamp + "\n" + accessKey).
func ncloudSign(method, path string, timestampMilli int64, accessKey, secretKey string) string {
	msg := fmt.Sprintf("%s %s\n%d\n%s", method, path, timestampMilli, accessKey)
	return hmacSHA256B64(secretKey, msg)
}

func ifEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
