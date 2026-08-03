package inbox

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
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

// DeleteMailbox removes a mailbox and its messages. Per-message raw RFC822
// and attachment blobs are cleaned up before the cascading row delete so the
// blob store does not leak orphaned bytes. The sweep lists with Offset: 0
// every iteration because each iteration deletes the rows it just listed — a
// growing offset would point past undeleted rows and skip them, leaking
// their blobs.
func (s *Service) DeleteMailbox(ctx context.Context, userID, id string) error {
	const pageSize = 200
	for {
		msgs, err := s.repo.ListMessages(ctx, ListFilter{
			UserID: userID, MailboxID: id, Limit: pageSize,
		})
		if err != nil {
			return err
		}
		if len(msgs) == 0 {
			break
		}
		for _, m := range msgs {
			// DeleteMessage already cleans the raw blob + every attachment
			// blob. ErrMessageNotFound is fine: a concurrent delete may have
			// raced.
			if err := s.DeleteMessage(ctx, userID, m.ID); err != nil && !errors.Is(err, ErrMessageNotFound) {
				return err
			}
		}
	}
	return s.repo.DeleteMailbox(ctx, userID, id)
}

// Ingest stores one inbound raw RFC822 message. Routing: the explicit
// recipientAddress (envelope) wins; otherwise the To/Cc headers are scanned for
// a mailbox this deployment owns. A message whose Message-ID is already stored
// in the resolved mailbox returns ErrDuplicate (callers treat as idempotent).
func (s *Service) Ingest(ctx context.Context, recipientAddress string, raw []byte, receivedAt time.Time) (*Message, error) {
	parsed, err := letter.Parse(raw)
	if err != nil {
		return nil, err
	}
	mb, err := s.resolveMailbox(ctx, recipientAddress, parsed)
	if err != nil {
		return nil, err
	}
	if exists, err := s.repo.ExistsByMessageID(ctx, mb.ID, parsed.MessageID); err != nil {
		return nil, err
	} else if exists {
		return nil, ErrDuplicate
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
		ID: nextID(), MailboxID: mb.ID, UserID: mb.UserID,
		MessageID: parsed.MessageID, InReplyTo: firstID(parsed.References),
		References: parsed.References,
		Subject:    parsed.Subject, From: from,
		To: parsed.To, Cc: parsed.Cc, Bcc: parsed.Bcc, ReplyTo: parsed.ReplyTo,
		Direction: DirectionInbound,
		RawPath:   rawPath, BodyText: parsed.Text, BodyHTML: parsed.HTML,
		SizeBytes: int64(len(raw)),
	}
	rt := receivedAt.UTC().Truncate(time.Second)
	msg.ReceivedAt = &rt
	if err := s.repo.CreateMessage(ctx, msg); err != nil {
		if errors.Is(err, ErrDuplicate) {
			if s.blobs != nil && rawPath != "" {
				_ = s.blobs.Delete(ctx, rawPath)
			}
			return nil, ErrDuplicate
		}
		if s.blobs != nil && rawPath != "" {
			_ = s.blobs.Delete(ctx, rawPath)
		}
		return nil, err
	}
	s.storeInboundAttachments(ctx, msg.ID, mb.UserID, parsed.Attachments)
	return msg, nil
}

// storeInboundAttachments saves each parsed MIME attachment to the blob store
// and records its metadata. Failures are logged best-effort: a missing
// attachment should not fail the whole ingest.
func (s *Service) storeInboundAttachments(ctx context.Context, messageID, userID string, atts []letter.ParsedAttachment) {
	for _, pa := range atts {
		if s.blobs == nil || len(pa.Data) == 0 {
			continue
		}
		ct := pa.ContentType
		if ct == "" {
			ct = "application/octet-stream"
		}
		path, err := s.blobs.Save(ctx, "mail-attachments", ct, bytes.NewReader(pa.Data))
		if err != nil {
			continue
		}
		a := &Attachment{
			ID: nextID(), MessageID: messageID, UserID: userID, BlobPath: path,
			Filename: pa.Filename, ContentType: ct, ContentID: pa.ContentID,
			Inline: pa.Disposition == "inline", SizeBytes: int64(len(pa.Data)),
		}
		if err := s.repo.CreateAttachment(ctx, a); err != nil {
			_ = s.blobs.Delete(ctx, path)
		}
	}
}

// resolveMailbox finds the destination mailbox, preferring the envelope
// recipient and falling back to the first To/Cc address this deployment owns.
func (s *Service) resolveMailbox(ctx context.Context, recipient string, p *letter.ParsedMessage) (*Mailbox, error) {
	if recipient = strings.ToLower(strings.TrimSpace(recipient)); recipient != "" {
		if mb, err := s.repo.GetMailboxByAddress(ctx, recipient); err == nil {
			return mb, nil
		}
	}
	for _, a := range append(append([]letter.Address{}, p.To...), p.Cc...) {
		if mb, err := s.repo.GetMailboxByAddress(ctx, strings.ToLower(a.Address)); err == nil {
			return mb, nil
		}
	}
	return nil, ErrMailboxNotFound
}

func firstID(refs []string) string {
	// In-Reply-To: pick the last referenced id (the immediate parent), if any.
	if len(refs) == 0 {
		return ""
	}
	return refs[len(refs)-1]
}

// Send validates an outgoing message, hands it to sender, then stores it.
// The message row is persisted with is_outbox=1 BEFORE the driver is called
// so a crash or DB outage after a successful SMTP/HTTP send still leaves a
// record in the user's mailbox; on success MarkSent flips is_outbox off. If
// sender is nil and no driver is configured, it returns ErrNoOutbound.
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
	// Resolve any pending attachments into the outgoing MIME tree.
	if len(in.AttachmentIDs) > 0 {
		atts, err := s.repo.ListByIDs(ctx, userID, in.AttachmentIDs)
		if err != nil {
			return nil, err
		}
		out.Attachments = make([]letter.Attachment, 0, len(atts))
		for _, a := range atts {
			data, err := s.readAttachmentData(ctx, a)
			if err != nil {
				return nil, fmt.Errorf("inbox: read attachment %q: %w", a.Filename, err)
			}
			if len(data) == 0 {
				return nil, fmt.Errorf("%w: attachment %q is empty", ErrInvalidInput, a.Filename)
			}
			out.Attachments = append(out.Attachments, letter.Attachment{
				Filename: a.Filename, ContentType: a.ContentType,
				ContentID: a.ContentID, Inline: a.Inline, Data: data,
			})
		}
	}
	raw, err := letter.BuildRFC822(out)
	if err != nil {
		return nil, err
	}
	out.MessageID = messageIDFromRaw(raw)

	var rawPath string
	if s.blobs != nil {
		p, err := s.blobs.Save(ctx, "mail-raw", "message/rfc822", strings.NewReader(string(raw)))
		if err != nil {
			return nil, fmt.Errorf("inbox: save raw: %w", err)
		}
		rawPath = p
	}
	msg := &Message{
		ID: nextID(), MailboxID: mb.ID, UserID: userID,
		MessageID: out.MessageID, InReplyTo: in.InReplyTo, References: in.References,
		Subject: in.Subject,
		From:    letter.Address{Name: mb.DisplayName, Address: mb.Address},
		To:      in.To, Cc: in.Cc, Bcc: in.Bcc, ReplyTo: in.ReplyTo,
		Direction: DirectionOutbound,
		RawPath:   rawPath, BodyText: in.Text, BodyHTML: in.HTML,
		SizeBytes: int64(len(raw)),
		IsOutbox:  true,
	}
	if err := s.repo.CreateMessage(ctx, msg); err != nil {
		if s.blobs != nil && rawPath != "" {
			_ = s.blobs.Delete(ctx, rawPath)
		}
		return nil, err
	}
	if err := s.repo.LinkAttachments(ctx, userID, msg.ID, in.AttachmentIDs); err != nil {
		return nil, err
	}
	// Hand the message to the driver. On failure, leave the row in the
	// outbox (is_outbox=1) so it is still visible to the user and a future
	// sweeper can retry it; surface the error so the caller can react.
	if err := sender.Send(ctx, out); err != nil {
		return msg, fmt.Errorf("inbox: send via %s: %w", sender.Name(), err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	msg.SentAt = &now
	msg.IsOutbox = false
	// MarkSent is best-effort: the driver has already accepted the message.
	// A transient DB error or a concurrent retry that already flipped the
	// row must not turn this into a retry that produces a duplicate.
	_ = s.repo.MarkSent(ctx, userID, msg.ID, now)
	return msg, nil
}

// CreateAttachment stores a pending outbound attachment owned by userID.
func (s *Service) CreateAttachment(ctx context.Context, userID, filename, contentType string, r io.Reader) (Attachment, error) {
	if s.blobs == nil {
		return Attachment{}, fmt.Errorf("%w: blob store unavailable", ErrInvalidInput)
	}
	filename = strings.TrimSpace(filename)
	if filename == "" {
		return Attachment{}, fmt.Errorf("%w: filename required", ErrInvalidInput)
	}
	if contentType = strings.TrimSpace(contentType); contentType == "" {
		contentType = "application/octet-stream"
	}
	var buf bytes.Buffer
	n, err := io.Copy(&buf, io.LimitReader(r, 25<<20+1))
	if err != nil {
		return Attachment{}, fmt.Errorf("inbox: read attachment: %w", err)
	}
	if n == 0 {
		return Attachment{}, fmt.Errorf("%w: empty attachment", ErrInvalidInput)
	}
	if n > 25<<20 {
		return Attachment{}, fmt.Errorf("%w: attachment too large", ErrInvalidInput)
	}
	path, err := s.blobs.Save(ctx, "mail-attachments", contentType, bytes.NewReader(buf.Bytes()))
	if err != nil {
		return Attachment{}, fmt.Errorf("inbox: save attachment: %w", err)
	}
	a := Attachment{
		ID: nextID(), UserID: userID, BlobPath: path,
		Filename: filename, ContentType: contentType, SizeBytes: n,
	}
	if err := s.repo.CreateAttachment(ctx, &a); err != nil {
		_ = s.blobs.Delete(ctx, path)
		return Attachment{}, err
	}
	return a, nil
}

// ListAttachments returns metadata for one stored message.
func (s *Service) ListAttachments(ctx context.Context, userID, messageID string) ([]Attachment, error) {
	if _, err := s.repo.GetMessage(ctx, userID, messageID); err != nil {
		return nil, err
	}
	return s.repo.ListAttachmentsByMessage(ctx, userID, messageID)
}

// GetAttachment returns metadata and a readable payload for one attachment.
func (s *Service) GetAttachment(ctx context.Context, userID, id string) (Attachment, io.ReadCloser, error) {
	if s.blobs == nil {
		return Attachment{}, nil, ErrMessageNotFound
	}
	a, err := s.repo.GetAttachment(ctx, userID, id)
	if err != nil {
		return Attachment{}, nil, err
	}
	body, _, err := s.blobs.Open(ctx, a.BlobPath)
	if err != nil {
		return Attachment{}, nil, ErrMessageNotFound
	}
	return a, body, nil
}

// DeleteAttachment removes a pending attachment and its blob. Message-bound
// attachments remain part of the immutable message record.
func (s *Service) DeleteAttachment(ctx context.Context, userID, id string) error {
	a, err := s.repo.GetAttachment(ctx, userID, id)
	if err != nil {
		return err
	}
	if a.MessageID != "" {
		return fmt.Errorf("%w: sent or received attachments cannot be removed", ErrInvalidInput)
	}
	if err := s.repo.DeleteAttachment(ctx, userID, id); err != nil {
		return err
	}
	if s.blobs != nil {
		_ = s.blobs.Delete(ctx, a.BlobPath)
	}
	return nil
}

func (s *Service) readAttachmentData(ctx context.Context, a Attachment) ([]byte, error) {
	if s.blobs == nil || a.BlobPath == "" {
		return nil, ErrMessageNotFound
	}
	body, _, err := s.blobs.Open(ctx, a.BlobPath)
	if err != nil {
		return nil, err
	}
	defer body.Close()
	return io.ReadAll(io.LimitReader(body, 25<<20+1))
}

// SaveDraft stores a message as a draft (direction=outbound, is_draft=1)
// without invoking any outbound driver. When draftID is empty a new draft is
// created; otherwise the existing draft owned by the user is updated. Returns
// the stored draft.
func (s *Service) SaveDraft(ctx context.Context, userID string, draftID string, in SendInput) (*Message, error) {
	mb, err := s.repo.GetMailbox(ctx, userID, in.MailboxID)
	if err != nil {
		return nil, err
	}
	var rawPath string
	now := time.Now().UTC().Truncate(time.Second)

	if draftID != "" {
		existing, err := s.repo.GetMessage(ctx, userID, draftID)
		if err != nil {
			return nil, err
		}
		if !existing.IsDraft {
			return nil, fmt.Errorf("%w: message is not a draft", ErrInvalidInput)
		}
		existing.Subject = in.Subject
		existing.To = in.To
		existing.Cc = in.Cc
		existing.Bcc = in.Bcc
		existing.ReplyTo = in.ReplyTo
		existing.BodyText = in.Text
		existing.BodyHTML = in.HTML
		existing.InReplyTo = in.InReplyTo
		existing.References = in.References
		// Recompute thread id when the reply chain changed so the draft
		// re-parents under its parent conversation immediately, not only
		// when it is eventually sent. When the chain is cleared the draft
		// must become its own singleton conversation (not stay stranded in
		// the previous parent).
		tid := letter.ThreadKey("", in.InReplyTo, in.References)
		if tid != "" && tid != "no-id" {
			existing.ThreadID = tid
		} else {
			existing.ThreadID = existing.ID
		}
		existing.UpdatedAt = now
		existing.MailboxID = mb.ID
		if err := s.repo.UpdateDraft(ctx, existing); err != nil {
			return nil, err
		}
		if in.AttachmentIDs != nil {
			if err := s.repo.RebindAttachments(ctx, userID, existing.ID, in.AttachmentIDs); err != nil {
				return nil, err
			}
		}
		return existing, nil
	}

	draft := &Message{
		ID: nextID(), MailboxID: mb.ID, UserID: userID,
		MessageID: "", InReplyTo: in.InReplyTo, References: in.References,
		Subject: in.Subject,
		From:    letter.Address{Name: mb.DisplayName, Address: mb.Address},
		To:      in.To, Cc: in.Cc, Bcc: in.Bcc, ReplyTo: in.ReplyTo,
		Direction: DirectionOutbound,
		RawPath:   rawPath, BodyText: in.Text, BodyHTML: in.HTML,
		IsDraft: true,
	}
	// A reply draft should join its parent thread even before it has a
	// Message-ID; fall back to the draft's own id only for standalone drafts.
	if tid := letter.ThreadKey("", in.InReplyTo, in.References); tid != "" && tid != "no-id" {
		draft.ThreadID = tid
	} else {
		draft.ThreadID = draft.ID
	}
	if err := s.repo.CreateMessage(ctx, draft); err != nil {
		return nil, err
	}
	if len(in.AttachmentIDs) > 0 {
		if err := s.repo.LinkAttachments(ctx, userID, draft.ID, in.AttachmentIDs); err != nil {
			return nil, err
		}
	}
	return draft, nil
}

// SendDraft loads a stored draft, sends it via sender, then flips is_draft
// off and records the sent metadata. The draft row is flipped into the
// outbox (is_draft=0, is_outbox=1) BEFORE the driver is called so a crash
// or DB outage between send and persist leaves a recoverable row with a
// stable Message-ID (RetrySend re-uses the same id, so retries never
// duplicate).
func (s *Service) SendDraft(ctx context.Context, userID, draftID string, sender OutboundSender) (*Message, error) {
	draft, err := s.repo.GetMessage(ctx, userID, draftID)
	if err != nil {
		return nil, err
	}
	if !draft.IsDraft {
		return nil, fmt.Errorf("%w: message is not a draft", ErrInvalidInput)
	}
	if len(draft.To) == 0 {
		return nil, fmt.Errorf("%w: at least one recipient required", ErrInvalidInput)
	}
	if sender == nil {
		return nil, ErrNoOutbound
	}
	if draft.BodyText == "" && draft.BodyHTML == "" {
		return nil, fmt.Errorf("%w: message body required", ErrInvalidInput)
	}
	out := letter.Outgoing{
		From: draft.From, To: draft.To, Cc: draft.Cc, Bcc: draft.Bcc,
		ReplyTo: draft.ReplyTo, Subject: draft.Subject,
		Text: draft.BodyText, HTML: draft.BodyHTML,
		InReplyTo: draft.InReplyTo, References: draft.References,
	}
	atts, err := s.repo.ListAttachmentsByMessage(ctx, userID, draft.ID)
	if err != nil {
		return nil, err
	}
	for _, a := range atts {
		data, err := s.readAttachmentData(ctx, a)
		if err != nil {
			return nil, fmt.Errorf("inbox: read attachment %q: %w", a.Filename, err)
		}
		if len(data) == 0 {
			return nil, fmt.Errorf("%w: attachment %q is empty", ErrInvalidInput, a.Filename)
		}
		out.Attachments = append(out.Attachments, letter.Attachment{
			Filename: a.Filename, ContentType: a.ContentType,
			ContentID: a.ContentID, Inline: a.Inline, Data: data,
		})
	}
	raw, err := letter.BuildRFC822(out)
	if err != nil {
		return nil, err
	}
	out.MessageID = messageIDFromRaw(raw)

	// Pre-write: persist Message-ID, raw blob and size while the row is
	// still a draft (UpdateDraft's WHERE clause requires is_draft=1), then
	// flip the draft into the outbox before invoking the driver.
	draft.MessageID = out.MessageID
	if s.blobs != nil {
		p, err := s.blobs.Save(ctx, "mail-raw", "message/rfc822", strings.NewReader(string(raw)))
		if err != nil {
			return nil, fmt.Errorf("inbox: save raw: %w", err)
		}
		draft.RawPath = p
	}
	draft.SizeBytes = int64(len(raw))
	if tid := letter.ThreadKey(out.MessageID, draft.InReplyTo, draft.References); tid != "" && tid != "no-id" {
		draft.ThreadID = tid
	}
	if err := s.repo.UpdateDraft(ctx, draft); err != nil {
		return nil, err
	}
	if err := s.repo.MarkOutbox(ctx, userID, draft.ID); err != nil {
		return nil, err
	}
	draft.IsDraft = false
	draft.IsOutbox = true

	// Hand to the driver. On failure leave the row in the outbox for
	// RetrySend; surface the error so the caller can react.
	if err := sender.Send(ctx, out); err != nil {
		return draft, fmt.Errorf("inbox: send via %s: %w", sender.Name(), err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	draft.SentAt = &now
	draft.IsOutbox = false
	// MarkSent is best-effort: the driver has already accepted the message.
	// A transient DB error or a concurrent retry that already flipped the
	// row must not turn this into a retry that produces a duplicate.
	_ = s.repo.MarkSent(ctx, userID, draft.ID, now)
	return draft, nil
}

// ListThreads returns conversation summaries for the user.
func (s *Service) ListThreads(ctx context.Context, userID, mailboxID string, limit, offset int) ([]Thread, error) {
	return s.repo.ListThreads(ctx, userID, mailboxID, limit, offset)
}

// ListMessages returns messages for a mailbox/folder.
func (s *Service) ListMessages(ctx context.Context, f ListFilter) ([]*Message, error) {
	return s.repo.ListMessages(ctx, f)
}

// Counts returns per-folder totals for the user (optionally scoped to one
// mailbox), used for sidebar unread/draft/starred badges.
func (s *Service) Counts(ctx context.Context, userID, mailboxID string) (FolderCounts, error) {
	return s.repo.Counts(ctx, userID, mailboxID)
}

// GetMessage returns one message (ownership-scoped).
func (s *Service) GetMessage(ctx context.Context, userID, id string) (*Message, error) {
	return s.repo.GetMessage(ctx, userID, id)
}

// PatchFlags updates read/starred/draft flags.
func (s *Service) PatchFlags(ctx context.Context, userID, id string, patch FlagPatch) error {
	return s.repo.PatchFlags(ctx, userID, id, patch)
}

// RetrySend re-attempts delivery of an outbox message (one left at
// is_outbox=1 by a previous Send that failed at the driver). The stored row
// already carries the parsed fields and a saved raw blob; we rebuild the
// outgoing envelope, hand it to sender, and on success flip is_outbox off
// and stamp sent_at.
func (s *Service) RetrySend(ctx context.Context, userID, id string, sender OutboundSender) (*Message, error) {
	if sender == nil {
		return nil, ErrNoOutbound
	}
	msg, err := s.repo.GetMessage(ctx, userID, id)
	if err != nil {
		return nil, err
	}
	if !msg.IsOutbox {
		return nil, fmt.Errorf("%w: message is not in the outbox", ErrInvalidInput)
	}
	out := letter.Outgoing{
		From: msg.From, To: msg.To, Cc: msg.Cc, Bcc: msg.Bcc,
		ReplyTo: msg.ReplyTo, Subject: msg.Subject,
		Text: msg.BodyText, HTML: msg.BodyHTML,
		InReplyTo: msg.InReplyTo, References: msg.References,
		MessageID: msg.MessageID,
	}
	atts, err := s.repo.ListAttachmentsByMessage(ctx, userID, msg.ID)
	if err != nil {
		return nil, err
	}
	for _, a := range atts {
		data, err := s.readAttachmentData(ctx, a)
		if err != nil {
			return nil, fmt.Errorf("inbox: read attachment %q: %w", a.Filename, err)
		}
		if len(data) == 0 {
			return nil, fmt.Errorf("%w: attachment %q is empty", ErrInvalidInput, a.Filename)
		}
		out.Attachments = append(out.Attachments, letter.Attachment{
			Filename: a.Filename, ContentType: a.ContentType,
			ContentID: a.ContentID, Inline: a.Inline, Data: data,
		})
	}
	if err := sender.Send(ctx, out); err != nil {
		return msg, fmt.Errorf("inbox: retry send via %s: %w", sender.Name(), err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	msg.SentAt = &now
	msg.IsOutbox = false
	// MarkSent is best-effort: the driver has already accepted the message.
	// A transient DB error or a concurrent retry that already flipped the
	// row must not turn this into a retry that produces a duplicate.
	_ = s.repo.MarkSent(ctx, userID, msg.ID, now)
	return msg, nil
}

// DeleteMessage removes a message, its raw RFC822 blob and every attachment
// blob. Per-attachment rows cascade on the row delete but the blob store has
// no such trigger, so we sweep here.
func (s *Service) DeleteMessage(ctx context.Context, userID, id string) error {
	m, err := s.repo.GetMessage(ctx, userID, id)
	if err != nil {
		return err
	}
	atts, err := s.repo.ListAttachmentsByMessage(ctx, userID, id)
	if err != nil {
		return err
	}
	if err := s.repo.DeleteMessage(ctx, userID, id); err != nil {
		return err
	}
	if s.blobs != nil {
		if m.RawPath != "" {
			_ = s.blobs.Delete(ctx, m.RawPath)
		}
		for _, a := range atts {
			if a.BlobPath != "" {
				_ = s.blobs.Delete(ctx, a.BlobPath)
			}
		}
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
