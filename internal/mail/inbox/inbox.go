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
	// ErrDuplicate is returned by Ingest when the Message-ID is already stored
	// in the target mailbox. Callers should treat it as success (idempotent).
	ErrDuplicate = errors.New("inbox: duplicate message")
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
	MailboxID  string
	To         []letter.Address
	Cc         []letter.Address
	Bcc        []letter.Address
	ReplyTo    []letter.Address
	Subject    string
	Text       string
	HTML       string
	InReplyTo  string
	References []string
}
