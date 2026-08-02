package inbox

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/headercat/airbrew/internal/blob"
	"github.com/headercat/airbrew/internal/id"
	"github.com/headercat/airbrew/internal/mail/letter"
)

// OutboundSender is the minimal contract the inbox service needs to deliver a
// message. The outbound driver registry (internal/mail/outbound) satisfies it,
// keeping inbox decoupled from the concrete driver package.
type OutboundSender interface {
	Send(ctx context.Context, o letter.Outgoing) error
	Name() string
}

// Service contains mail business logic.
type Service struct {
	repo  *Repository
	blobs blob.Store
}

// NewService returns a Service backed by repo. blobs stores raw RFC822.
func NewService(repo *Repository, blobs blob.Store) *Service {
	return &Service{repo: repo, blobs: blobs}
}

// CreateMailbox provisions a new address for userID.
func (s *Service) CreateMailbox(ctx context.Context, in NewMailboxInput) (*Mailbox, error) {
	local, domain, err := splitAddress(in.Address)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidInput, err)
	}
	mb := &Mailbox{
		ID:          nextID(),
		UserID:      in.UserID,
		LocalPart:   local,
		Domain:      domain,
		Address:     local + "@" + domain,
		DisplayName: strings.TrimSpace(in.DisplayName),
		IsPrimary:   in.IsPrimary,
	}
	if err := s.repo.CreateMailbox(ctx, mb); err != nil {
		return nil, err
	}
	return mb, nil
}

// ListMailboxes returns the user's mailboxes.
func (s *Service) ListMailboxes(ctx context.Context, userID string) ([]*Mailbox, error) {
	return s.repo.ListMailboxes(ctx, userID)
}

// GetMailbox returns one mailbox (ownership-scoped).
func (s *Service) GetMailbox(ctx context.Context, userID, id string) (*Mailbox, error) {
	return s.repo.GetMailbox(ctx, userID, id)
}

// DeleteMailbox removes a mailbox and its messages.
func (s *Service) DeleteMailbox(ctx context.Context, userID, id string) error {
	return s.repo.DeleteMailbox(ctx, userID, id)
}

// Ingest stores one inbound raw RFC822 message, routing it to the mailbox that
// owns recipientAddress. Returns ErrMailboxNotFound if no mailbox matches.
func (s *Service) Ingest(ctx context.Context, recipientAddress string, raw []byte, receivedAt time.Time) (*Message, error) {
	recipientAddress = strings.ToLower(strings.TrimSpace(recipientAddress))
	mb, err := s.repo.GetMailboxByAddress(ctx, recipientAddress)
	if err != nil {
		return nil, err
	}
	parsed, err := letter.Parse(raw)
	if err != nil {
		return nil, err
	}
	if receivedAt.IsZero() {
		receivedAt = time.Now().UTC()
	}
	if !parsed.Date.IsZero() {
		receivedAt = parsed.Date
	}
	var rawPath string
	if s.blobs != nil {
		p, err := s.blobs.Save(ctx, "mail-raw", "message/rfc822", strings.NewReader(string(raw)))
		if err != nil {
			return nil, fmt.Errorf("inbox: save raw: %w", err)
		}
		rawPath = p
	}
	from := letter.Address{}
	if len(parsed.From) > 0 {
		from = parsed.From[0]
	}
	if from.Address == "" {
		from.Address = "unknown@"
	}
	msg := &Message{
		ID:        nextID(),
		MailboxID: mb.ID,
		UserID:    mb.UserID,
		MessageID: parsed.MessageID,
		Subject:   parsed.Subject,
		From:      from,
		To:        parsed.To,
		Cc:        parsed.Cc,
		Bcc:       parsed.Bcc,
		ReplyTo:   parsed.ReplyTo,
		Direction: DirectionInbound,
		RawPath:   rawPath,
		BodyText:  parsed.Text,
		BodyHTML:  parsed.HTML,
		SizeBytes: int64(len(raw)),
	}
	rt := receivedAt.UTC().Truncate(time.Second)
	msg.ReceivedAt = &rt
	if err := s.repo.CreateMessage(ctx, msg); err != nil {
		if s.blobs != nil && rawPath != "" {
			_ = s.blobs.Delete(ctx, rawPath)
		}
		return nil, err
	}
	return msg, nil
}

// Send validates an outgoing message, hands it to sender, then stores it.
// If sender is nil and no driver is configured, it returns ErrNoOutbound.
func (s *Service) Send(ctx context.Context, userID string, in SendInput, sender OutboundSender) (*Message, error) {
	if len(in.To) == 0 {
		return nil, fmt.Errorf("%w: at least one recipient required", ErrInvalidInput)
	}
	mb, err := s.repo.GetMailbox(ctx, userID, in.MailboxID)
	if err != nil {
		return nil, err
	}
	if sender == nil {
		return nil, ErrNoOutbound
	}
	out := letter.Outgoing{
		From: letter.Address{Name: mb.DisplayName, Address: mb.Address},
		To:   in.To, Cc: in.Cc, Bcc: in.Bcc, ReplyTo: in.ReplyTo,
		Subject: in.Subject, Text: in.Text, HTML: in.HTML,
		InReplyTo: in.InReplyTo, References: in.References,
	}
	if out.Text == "" && out.HTML == "" {
		return nil, fmt.Errorf("%w: message body required", ErrInvalidInput)
	}
	raw, err := letter.BuildRFC822(out)
	if err != nil {
		return nil, err
	}
	out.MessageID = messageIDFromRaw(raw)
	if err := sender.Send(ctx, out); err != nil {
		return nil, fmt.Errorf("inbox: send via %s: %w", sender.Name(), err)
	}
	var rawPath string
	if s.blobs != nil {
		p, err := s.blobs.Save(ctx, "mail-raw", "message/rfc822", strings.NewReader(string(raw)))
		if err != nil {
			return nil, fmt.Errorf("inbox: save raw: %w", err)
		}
		rawPath = p
	}
	now := time.Now().UTC().Truncate(time.Second)
	msg := &Message{
		ID: nextID(), MailboxID: mb.ID, UserID: userID,
		MessageID: out.MessageID, Subject: in.Subject,
		From: letter.Address{Name: mb.DisplayName, Address: mb.Address},
		To:   in.To, Cc: in.Cc, Bcc: in.Bcc, ReplyTo: in.ReplyTo,
		Direction: DirectionOutbound,
		RawPath:   rawPath, BodyText: in.Text, BodyHTML: in.HTML,
		SizeBytes: int64(len(raw)),
	}
	msg.SentAt = &now
	if err := s.repo.CreateMessage(ctx, msg); err != nil {
		if s.blobs != nil && rawPath != "" {
			_ = s.blobs.Delete(ctx, rawPath)
		}
		return nil, err
	}
	return msg, nil
}

// ListMessages returns messages for a mailbox/folder.
func (s *Service) ListMessages(ctx context.Context, f ListFilter) ([]*Message, error) {
	return s.repo.ListMessages(ctx, f)
}

// GetMessage returns one message (ownership-scoped).
func (s *Service) GetMessage(ctx context.Context, userID, id string) (*Message, error) {
	return s.repo.GetMessage(ctx, userID, id)
}

// PatchFlags updates read/starred/draft flags.
func (s *Service) PatchFlags(ctx context.Context, userID, id string, patch FlagPatch) error {
	return s.repo.PatchFlags(ctx, userID, id, patch)
}

// DeleteMessage removes a message and its raw blob.
func (s *Service) DeleteMessage(ctx context.Context, userID, id string) error {
	m, err := s.repo.GetMessage(ctx, userID, id)
	if err != nil {
		return err
	}
	if err := s.repo.DeleteMessage(ctx, userID, id); err != nil {
		return err
	}
	if s.blobs != nil && m.RawPath != "" {
		_ = s.blobs.Delete(ctx, m.RawPath)
	}
	return nil
}

// ErrNoOutbound is returned when sending with no active outbound driver.
var ErrNoOutbound = errors.New("inbox: no active outbound provider")

func splitAddress(addr string) (local, domain string, err error) {
	addr = strings.ToLower(strings.TrimSpace(addr))
	at := strings.LastIndex(addr, "@")
	if at <= 0 || at == len(addr)-1 {
		return "", "", errors.New("invalid email address")
	}
	local = addr[:at]
	domain = addr[at+1:]
	if strings.ContainsAny(local, " \t<>(),;:\"") {
		return "", "", errors.New("invalid local part")
	}
	if !strings.Contains(domain, ".") {
		return "", "", errors.New("invalid domain")
	}
	return local, domain, nil
}

// messageIDFromRaw extracts the Message-ID (sans angle brackets) from a built
// RFC822 message, falling back to a generated value.
func messageIDFromRaw(raw []byte) string {
	parsed, err := letter.Parse(raw)
	if err == nil && parsed.MessageID != "" {
		return parsed.MessageID
	}
	return id.New()
}
