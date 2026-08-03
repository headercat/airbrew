package outbound

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/smtp"
	"strings"
	"time"

	"github.com/headercat/airbrew/internal/mail/letter"
)

func init() { Register("smtp", buildSMTP) }

// SMTPConfig connects to an external SMTP relay. When TLS is "starttls" the
// client issues STARTTLS after EHLO; when "tls" it opens a TLS connection
// directly (SMTPS); otherwise the connection is plaintext (not recommended).
type SMTPConfig struct {
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Username string `json:"username"`
	Password string `json:"password"`
	From     string `json:"from"`      // override envelope-from; defaults to message From
	TLS      string `json:"tls"`       // "", "starttls", "tls"
	Helo     string `json:"helo_name"` // EHLO hostname; defaults to "airbrew"
}

type smtpDriver struct{ cfg SMTPConfig }

func buildSMTP(raw json.RawMessage) (Outbounder, error) {
	var cfg SMTPConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, fmt.Errorf("smtp: bad config: %w", err)
	}
	if cfg.Host == "" || cfg.Port == 0 {
		return nil, fmt.Errorf("smtp: host and port required")
	}
	return &smtpDriver{cfg: cfg}, nil
}

func (d *smtpDriver) Name() string { return "smtp" }

func (d *smtpDriver) Send(ctx context.Context, o letter.Outgoing) error {
	recipients := o.Recipients()
	if len(recipients) == 0 {
		return errors.New("smtp: no recipients")
	}
	raw, err := letter.BuildRFC822(o)
	if err != nil {
		return err
	}
	from := d.cfg.From
	if from == "" {
		from = o.From.Address
	}
	addr := net.JoinHostPort(d.cfg.Host, fmt.Sprintf("%d", d.cfg.Port))
	return d.deliver(ctx, addr, from, recipients, raw)
}

func (d *smtpDriver) deliver(ctx context.Context, addr, from string, recipients []string, raw []byte) error {
	var conn net.Conn
	var err error
	tlsCfg := &tls.Config{ServerName: d.cfg.Host}
	switch strings.ToLower(d.cfg.TLS) {
	case "tls":
		conn, err = tls.DialWithDialer(&net.Dialer{Timeout: 15 * time.Second}, "tcp", addr, tlsCfg)
		if err != nil {
			return fmt.Errorf("smtp: dial tls: %w", err)
		}
	default:
		conn, err = (&net.Dialer{Timeout: 15 * time.Second}).DialContext(ctx, "tcp", addr)
		if err != nil {
			return fmt.Errorf("smtp: dial: %w", err)
		}
	}
	stop := make(chan struct{})
	defer close(stop)
	// ctx cancellation: force-close the conn so any blocking read/write in
	// the SMTP client returns immediately and the goroutine running Send
	// unblocks. Otherwise Send could keep delivering after the caller has
	// given up (the HTTP request was cancelled), producing a duplicate when
	// the user retries.
	go func() {
		select {
		case <-ctx.Done():
			_ = conn.Close()
		case <-stop:
		}
	}()
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(60 * time.Second))

	c, err := smtp.NewClient(conn, d.cfg.Host)
	if err != nil {
		return fmt.Errorf("smtp: client: %w", err)
	}
	defer c.Close()

	helo := d.cfg.Helo
	if helo = strings.TrimSpace(helo); helo == "" {
		helo = "airbrew"
	}
	if err := c.Hello(helo); err != nil {
		return fmt.Errorf("smtp: hello: %w", err)
	}
	if strings.ToLower(d.cfg.TLS) == "starttls" {
		if ok, _ := c.Extension("STARTTLS"); !ok {
			return errors.New("smtp: server does not support STARTTLS")
		}
		if err := c.StartTLS(tlsCfg); err != nil {
			return fmt.Errorf("smtp: starttls: %w", err)
		}
	}
	if d.cfg.Username != "" {
		auth := smtp.PlainAuth("", d.cfg.Username, d.cfg.Password, d.cfg.Host)
		if err := c.Auth(auth); err != nil {
			return fmt.Errorf("smtp: auth: %w", err)
		}
	}
	if err := c.Mail(from); err != nil {
		return fmt.Errorf("smtp: mail from: %w", err)
	}
	for _, rcpt := range recipients {
		if err := c.Rcpt(rcpt); err != nil {
			return fmt.Errorf("smtp: rcpt to %s: %w", rcpt, err)
		}
	}
	w, err := c.Data()
	if err != nil {
		return fmt.Errorf("smtp: data: %w", err)
	}
	if _, err := w.Write(raw); err != nil {
		return fmt.Errorf("smtp: write body: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("smtp: close body: %w", err)
	}
	// The message is queued — the server has already replied 250 OK to
	// the final dot. A subsequent QUIT failure (network drop, ctx cancel
	// firing in the millisecond window) must NOT turn the send into an
	// error: that would leave the row in the outbox and a RetrySend would
	// deliver the same message a second time.
	_ = c.Quit()
	return nil
}
