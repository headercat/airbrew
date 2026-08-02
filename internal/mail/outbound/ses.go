package outbound

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/headercat/airbrew/internal/mail/letter"
)

func init() { Register("ses", buildSES) }

// SESConfig authenticates with the AWS SES Query API (SendRawEmail).
type SESConfig struct {
	AccessKey string `json:"access_key"`
	SecretKey string `json:"secret_key"`
	Region    string `json:"region"` // e.g. "us-east-1"
}

type sesDriver struct {
	cfg    SESConfig
	host   string
	client *http.Client
}

func buildSES(raw json.RawMessage) (Outbounder, error) {
	var cfg SESConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, fmt.Errorf("ses: bad config: %w", err)
	}
	if cfg.AccessKey == "" || cfg.SecretKey == "" || cfg.Region == "" {
		return nil, fmt.Errorf("ses: access_key, secret_key and region required")
	}
	return &sesDriver{
		cfg:    cfg,
		host:   "email." + cfg.Region + ".amazonaws.com",
		client: &http.Client{Timeout: 30 * time.Second},
	}, nil
}

func (d *sesDriver) Name() string { return "ses" }

func (d *sesDriver) Send(ctx context.Context, o letter.Outgoing) error {
	raw, err := letter.BuildRFC822(o)
	if err != nil {
		return err
	}
	values := url.Values{}
	values.Set("Action", "SendRawEmail")
	values.Set("Version", "2012-10-17")
	values.Set("Source", o.From.Address)
	values.Set("RawMessage.Data", b64(raw))
	for i, a := range o.To {
		values.Set(fmt.Sprintf("Destinations.member.%d", i+1), a.Address)
	}
	for i, a := range o.Cc {
		values.Set(fmt.Sprintf("Destinations.member.%d", len(o.To)+i+1), a.Address)
	}
	for i, a := range o.Bcc {
		values.Set(fmt.Sprintf("Destinations.member.%d", len(o.To)+len(o.Cc)+i+1), a.Address)
	}
	body := values.Encode()

	auth, amzDate, contentSHA := signAWSV4(http.MethodPost, d.host, d.cfg.Region, "ses", body, d.cfg.AccessKey, d.cfg.SecretKey, time.Now())

	endpoint := "https://" + d.host + "/"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Host", d.host)
	req.Header.Set("X-Amz-Date", amzDate)
	req.Header.Set("X-Amz-Content-Sha256", contentSHA)
	req.Header.Set("Authorization", auth)

	resp, err := d.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("ses: %s: %s", resp.Status, readBodySnippet(resp))
	}
	return nil
}
