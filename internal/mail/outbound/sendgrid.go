package outbound

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/headercat/airbrew/internal/mail/letter"
)

func init() { Register("sendgrid", buildSendgrid) }

// SendgridConfig authenticates with the v3 Web API.
type SendgridConfig struct {
	APIKey   string `json:"api_key"`
	Endpoint string `json:"endpoint"` // override; defaults to the v3 endpoint
}

type sendgridDriver struct {
	cfg      SendgridConfig
	endpoint string
	client   *http.Client
}

func buildSendgrid(raw json.RawMessage) (Outbounder, error) {
	var cfg SendgridConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, fmt.Errorf("sendgrid: bad config: %w", err)
	}
	if cfg.APIKey == "" {
		return nil, fmt.Errorf("sendgrid: api_key required")
	}
	endpoint := cfg.Endpoint
	if endpoint == "" {
		endpoint = "https://api.sendgrid.com/v3/mail/send"
	}
	return &sendgridDriver{cfg: cfg, endpoint: endpoint, client: &http.Client{Timeout: 30 * time.Second}}, nil
}

func (d *sendgridDriver) Name() string { return "sendgrid" }

func (d *sendgridDriver) Send(ctx context.Context, o letter.Outgoing) error {
	body := map[string]any{
		"personalizations": []map[string]any{{
			"to":            toSG(o.To),
			"cc":            toSG(o.Cc),
			"bcc":           toSG(o.Bcc),
			"subject":       o.Subject,
			"substitutions": map[string]string{},
		}},
		"from":          sgAddr(o.From),
		"reply_to":      sgAddr(firstOr(o.ReplyTo)),
		"content":       sgContent(o),
		"headers":       sgHeaders(o),
		"mail_settings": map[string]any{"sandbox_mode": map[string]bool{"enable": false}},
	}
	if atts := sgAttachments(o.Attachments); len(atts) > 0 {
		body["attachments"] = atts
	}
	buf, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.endpoint, bytes.NewReader(buf))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+d.cfg.APIKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := d.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("sendgrid: %s: %s", resp.Status, readBodySnippet(resp))
	}
	return nil
}

func toSG(in []letter.Address) []map[string]string {
	out := make([]map[string]string, 0, len(in))
	for _, a := range in {
		out = append(out, sgAddr(a))
	}
	return out
}

func sgAddr(a letter.Address) map[string]string {
	if a.Address == "" {
		return nil
	}
	return map[string]string{"email": a.Address, "name": a.Name}
}

func firstOr(in []letter.Address) letter.Address {
	if len(in) == 0 {
		return letter.Address{}
	}
	return in[0]
}

func sgContent(o letter.Outgoing) []map[string]string {
	var c []map[string]string
	if o.Text != "" {
		c = append(c, map[string]string{"type": "text/plain", "value": o.Text})
	}
	if o.HTML != "" {
		c = append(c, map[string]string{"type": "text/html", "value": o.HTML})
	}
	return c
}

func sgAttachments(in []letter.Attachment) []map[string]string {
	out := make([]map[string]string, 0, len(in))
	for _, a := range in {
		if len(a.Data) == 0 {
			continue
		}
		ct := a.ContentType
		if ct == "" {
			ct = "application/octet-stream"
		}
		disposition := "attachment"
		if a.Inline {
			disposition = "inline"
		}
		item := map[string]string{
			"content":     b64(a.Data),
			"type":        ct,
			"filename":    a.Filename,
			"disposition": disposition,
		}
		if a.ContentID != "" {
			item["content_id"] = a.ContentID
		}
		out = append(out, item)
	}
	return out
}

func sgHeaders(o letter.Outgoing) map[string]string {
	h := map[string]string{}
	if o.InReplyTo != "" {
		h["In-Reply-To"] = "<" + strings.Trim(o.InReplyTo, "<> ") + ">"
	}
	if len(o.References) > 0 {
		h["References"] = strings.Join(o.References, " ")
	}
	if len(h) == 0 {
		return nil
	}
	return h
}
