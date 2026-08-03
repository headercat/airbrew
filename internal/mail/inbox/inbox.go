// Package inbox holds the mail domain model, persistence and service: a user's
// mailboxes (email addresses) and the messages they hold, plus the ingest
// pipeline that turns raw RFC822 into stored rows and the send pipeline that
// hands an outgoing message to the active outbound driver.
package inbox

import (
	"errors"
	"time"

	"github.com/headercat/airbrew/internal/mail/letter"
)

// Sentinel errors.
var (
	// ErrMailboxNotFound is returned when no mailbox matches the lookup.
	ErrMailboxNotFound = errors.New("inbox: mailbox not found")
	// ErrMessageNotFound is returned when no message matches the lookup.
	ErrMessageNotFound = errors.New("inbox: message not found")
	// ErrAddressTaken is returned when creating a mailbox whose address exists.
	ErrAddressTaken = errors.New("inbox: address already taken")
	// ErrInvalidInput is returned on shape-validation failure.
	ErrInvalidInput = errors.New("inbox: invalid input")
	// ErrParseFailed wraps a MIME parse failure. Inbound pollers treat it as
	// persistent (mark the upstream message seen so it is not re-fetched and
	// re-parsed forever), unlike a transient DB/IO error which they retry.
	ErrParseFailed = errors.New("inbox: message parse failed")
	// ErrRetryInProgress is returned by RetrySend when another worker (the
	// background sweeper or a concurrent manual retry) has already claimed the
	// outbox row. Callers should treat it as "try later", not a hard failure.
	ErrRetryInProgress = errors.New("inbox: retry already in progress")
	// ErrDuplicate is returned by Ingest when the Message-ID is already stored
	// in the target mailbox. Callers should treat it as success (idempotent).
	ErrDuplicate = errors.New("inbox: duplicate message")
	// ErrProbe is returned by a discard ingester used for admin connectivity
	// probes. Poll drivers treat it as a signal to abort the sweep
	// immediately, WITHOUT recording the message as seen and WITHOUT
	// honouring delete_after_fetch — both of which would otherwise mutate or
	// destroy real upstream mail.
	ErrProbe = errors.New("inbox: probe stop")
)

// Mailbox is one email address owned by a user.
type Mailbox struct {
	ID          string
	UserID      string
	LocalPart   string
	Domain      string
	Address     string // lowercased local@domain
	DisplayName string
	IsPrimary   bool
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// Direction is whether a message was received or sent.
type Direction string

const (
	DirectionInbound  Direction = "inbound"
	DirectionOutbound Direction = "outbound"
)

// Message is a single stored email (received or sent).
type Message struct {
	ID         string
	MailboxID  string
	UserID     string
	MessageID  string
	ThreadID   string
	InReplyTo  string
	References []string
	Subject    string
	From       letter.Address
	To         []letter.Address
	Cc         []letter.Address
	Bcc        []letter.Address
	ReplyTo    []letter.Address
	Direction  Direction
	RawPath    string
	BodyText   string
	BodyHTML   string
	IsRead     bool
	IsStarred  bool
	IsDraft    bool
	IsOutbox   bool
	SizeBytes  int64
	ReceivedAt *time.Time
	SentAt     *time.Time
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// FolderCounts holds per-folder message totals for a mailbox or user.
type FolderCounts struct {
	Inbox   int `json:"inbox"`
	Sent    int `json:"sent"`
	Draft   int `json:"draft"`
	Starred int `json:"starred"`
	Unread  int `json:"unread"`
	Outbox  int `json:"outbox"`
}

// Thread is a conversation summary: the latest message in the group plus counts.
type Thread struct {
	ThreadID    string
	Subject     string
	From        letter.Address
	LastAt      time.Time
	Count       int
	UnreadCount int
}

// NewMailboxInput carries the editable fields for creating a mailbox.
type NewMailboxInput struct {
	UserID      string
	Address     string
	DisplayName string
	IsPrimary   bool
}

// SendInput carries the fields of an outbound message, validated before the
// active outbound driver is invoked.
type SendInput struct {
	MailboxID     string
	To            []letter.Address
	Cc            []letter.Address
	Bcc           []letter.Address
	ReplyTo       []letter.Address
	Subject       string
	Text          string
	HTML          string
	InReplyTo     string
	References    []string
	AttachmentIDs []string // pending attachments uploaded by the composer
}

// Attachment is one file attached to a message. The binary payload lives in the
// blob store at BlobPath; this struct is the stored metadata.
type Attachment struct {
	ID          string
	MessageID   string // empty while a pending outbound upload
	UserID      string
	BlobPath    string
	Filename    string
	ContentType string
	ContentID   string
	Inline      bool
	SizeBytes   int64
	CreatedAt   time.Time
}
