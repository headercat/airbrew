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
	From     string `json:"from"` // override envelope-from; defaults to message From
	TLS      string `json:"tls"`  // "", "starttls", "tls"
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

	done := make(chan error, 1)
	go func() { done <- d.deliver(addr, from, recipients, raw) }()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-done:
		return err
	}
}

func (d *smtpDriver) deliver(addr, from string, recipients []string, raw []byte) error {
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
		conn, err = net.DialTimeout("tcp", addr, 15*time.Second)
		if err != nil {
			return fmt.Errorf("smtp: dial: %w", err)
		}
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(60 * time.Second))

	c, err := smtp.NewClient(conn, d.cfg.Host)
	if err != nil {
		return fmt.Errorf("smtp: client: %w", err)
	}
	defer c.Close()

	if err := c.Hello("airbrew.local"); err != nil {
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
	return c.Quit()
}
