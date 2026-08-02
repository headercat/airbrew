package outbound

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"strings"
	"time"

	"github.com/headercat/airbrew/internal/mail/letter"
)

func init() { Register("mailgun", buildMailgun) }

// MailgunConfig authenticates with the Messages API.
type MailgunConfig struct {
	APIKey string `json:"api_key"` // the "key-..." private API key
	Domain string `json:"domain"`  // sending domain
	Region string `json:"region"`  // "us" (default) or "eu"
}

type mailgunDriver struct {
	cfg    MailgunConfig
	base   string
	client *http.Client
}

func buildMailgun(raw json.RawMessage) (Outbounder, error) {
	var cfg MailgunConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, fmt.Errorf("mailgun: bad config: %w", err)
	}
	if cfg.APIKey == "" || cfg.Domain == "" {
		return nil, fmt.Errorf("mailgun: api_key and domain required")
	}
	base := "https://api.mailgun.net/v3"
	if strings.EqualFold(cfg.Region, "eu") {
		base = "https://api.eu.mailgun.net/v3"
	}
	return &mailgunDriver{cfg: cfg, base: base, client: &http.Client{Timeout: 30 * time.Second}}, nil
}

func (d *mailgunDriver) Name() string { return "mailgun" }

func (d *mailgunDriver) Send(ctx context.Context, o letter.Outgoing) error {
	endpoint := fmt.Sprintf("%s/%s/messages", d.base, d.cfg.Domain)
	body, ct := mailgunBody(o)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", ct)
	req.SetBasicAuth("api", d.cfg.APIKey)
	resp, err := d.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("mailgun: %s: %s", resp.Status, readBodySnippet(resp))
	}
	return nil
}

// mailgunBody builds a multipart/form-data body carrying the raw RFC822 as the
// "message" field — Mailgun's most reliable path, preserving all headers.
func mailgunBody(o letter.Outgoing) (io.Reader, string) {
	raw, _ := letter.BuildRFC822(o)
	pr, pw := io.Pipe()
	mw := multipart.NewWriter(pw)
	go func() {
		_ = mw.WriteField("from", o.From.String())
		_ = mw.WriteField("to", joinComma(o.To))
		if len(o.Cc) > 0 {
			_ = mw.WriteField("cc", joinComma(o.Cc))
		}
		if len(o.Bcc) > 0 {
			_ = mw.WriteField("bcc", joinComma(o.Bcc))
		}
		_ = mw.WriteField("subject", o.Subject)
		h := textproto.MIMEHeader{}
		h.Set("Content-Disposition", `form-data; name="message"`)
		h.Set("Content-Type", "message/rfc822")
		part, err := mw.CreatePart(h)
		if err == nil {
			_, _ = part.Write(raw)
		}
		_ = mw.Close()
		_ = pw.Close()
	}()
	return pr, mw.FormDataContentType()
}

func joinComma(in []letter.Address) string {
	parts := make([]string, 0, len(in))
	for _, a := range in {
		parts = append(parts, a.String())
	}
	return strings.Join(parts, ", ")
}
