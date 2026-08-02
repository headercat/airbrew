package inbox

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/headercat/airbrew/internal/id"
	"github.com/headercat/airbrew/internal/mail/letter"
)

// Repository persists mailboxes and messages.
type Repository struct {
	db *sql.DB
}

// NewRepository returns a Repository bound to db.
func NewRepository(db *sql.DB) *Repository { return &Repository{db: db} }

const mailboxColumns = `id, user_id, local_part, domain, address,
	COALESCE(display_name,''), is_primary, created_at, updated_at`

// CreateMailbox inserts a mailbox. It returns ErrAddressTaken if the address is
// already in use by any user.
func (r *Repository) CreateMailbox(ctx context.Context, mb *Mailbox) error {
	now := time.Now().UTC().Truncate(time.Second)
	mb.CreatedAt = now
	mb.UpdatedAt = now
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO mailboxes (id, user_id, local_part, domain, address,
		  display_name, is_primary, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		mb.ID, mb.UserID, mb.LocalPart, mb.Domain, mb.Address,
		nullable(mb.DisplayName), boolToInt(mb.IsPrimary), now, now,
	)
	if err != nil && isUniqueViolation(err) {
		return ErrAddressTaken
	}
	if err != nil {
		return fmt.Errorf("inbox: insert mailbox: %w", err)
	}
	return nil
}

// GetMailboxByAddress returns the mailbox owning address (case-insensitive).
func (r *Repository) GetMailboxByAddress(ctx context.Context, address string) (*Mailbox, error) {
	return r.queryOneMailbox(ctx, "SELECT "+mailboxColumns+" FROM mailboxes WHERE address = ?",
		strings.ToLower(strings.TrimSpace(address)))
}

// GetMailbox returns one mailbox owned by userID.
func (r *Repository) GetMailbox(ctx context.Context, userID, id string) (*Mailbox, error) {
	return r.queryOneMailbox(ctx, "SELECT "+mailboxColumns+" FROM mailboxes WHERE id = ? AND user_id = ?", id, userID)
}

// ListMailboxes returns every mailbox owned by userID.
func (r *Repository) ListMailboxes(ctx context.Context, userID string) ([]*Mailbox, error) {
	rows, err := r.db.QueryContext(ctx,
		"SELECT "+mailboxColumns+" FROM mailboxes WHERE user_id = ? ORDER BY is_primary DESC, created_at ASC", userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Mailbox
	for rows.Next() {
		mb, err := scanMailbox(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, mb)
	}
	return out, rows.Err()
}

// DeleteMailbox removes a mailbox and its messages.
func (r *Repository) DeleteMailbox(ctx context.Context, userID, id string) error {
	res, err := r.db.ExecContext(ctx,
		"DELETE FROM mailboxes WHERE id = ? AND user_id = ?", id, userID)
	if err != nil {
		return fmt.Errorf("inbox: delete mailbox: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrMailboxNotFound
	}
	return nil
}

// CreateMessage inserts a message row.
func (r *Repository) CreateMessage(ctx context.Context, m *Message) error {
	now := time.Now().UTC().Truncate(time.Second)
	m.CreatedAt = now
	m.UpdatedAt = now
	to, _ := letter.MarshalAddresses(m.To)
	cc, _ := letter.MarshalAddresses(m.Cc)
	bcc, _ := letter.MarshalAddresses(m.Bcc)
	rt, _ := letter.MarshalAddresses(m.ReplyTo)
	from, _ := letter.MarshalAddresses([]letter.Address{m.From})
	if from == "" {
		from = "{}"
	}
	refs, _ := json.Marshal(m.References)
	if m.ThreadID == "" {
		m.ThreadID = letter.ThreadKey(m.MessageID, m.InReplyTo, m.References)
	}
	if _, err := r.db.ExecContext(ctx, `
		INSERT INTO mail_messages
		  (id, mailbox_id, user_id, message_id, thread_id, in_reply_to, refs, subject,
		   from_addr, to_addrs, cc_addrs, bcc_addrs, reply_to_addrs,
		   direction, raw_path, body_text, body_html,
		   is_read, is_starred, is_draft, is_outbox, size_bytes,
		   received_at, sent_at, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		m.ID, m.MailboxID, m.UserID, nullable(m.MessageID), nullable(m.ThreadID), nullable(m.InReplyTo), string(refs), nullable(m.Subject),
		from, to, cc, bcc, rt,
		string(m.Direction), nullable(m.RawPath), nullable(m.BodyText), nullable(m.BodyHTML),
		boolToInt(m.IsRead), boolToInt(m.IsStarred), boolToInt(m.IsDraft), boolToInt(m.IsOutbox), m.SizeBytes,
		nullableTime(m.ReceivedAt), nullableTime(m.SentAt), now, now,
	); err != nil {
		if isUniqueViolation(err) {
			return ErrDuplicate
		}
		return fmt.Errorf("inbox: insert message: %w", err)
	}
	return nil
}

// ExistsByMessageID reports whether a message with the same Message-ID is
// already stored in mailboxID. Used to short-circuit ingest before parsing.
func (r *Repository) ExistsByMessageID(ctx context.Context, mailboxID, messageID string) (bool, error) {
	if messageID == "" {
		return false, nil
	}
	var n int
	err := r.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM mail_messages WHERE mailbox_id = ? AND message_id = ?",
		mailboxID, messageID,
	).Scan(&n)
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// GetMessage returns one message owned by userID.
func (r *Repository) GetMessage(ctx context.Context, userID, id string) (*Message, error) {
	row := r.db.QueryRowContext(ctx,
		"SELECT "+messageColumns+" FROM mail_messages WHERE id = ? AND user_id = ?", id, userID)
	m, err := scanMessage(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrMessageNotFound
	}
	return m, err
}

// ListFilter controls which messages are returned for a mailbox.
type ListFilter struct {
	UserID    string
	MailboxID string
	Direction Direction
	Folder    string // "inbox", "sent", "draft", "starred", "unread"
	ThreadID  string // restrict to one conversation
	Limit     int
	Offset    int
}

// ListMessages returns messages for a mailbox matching the filter, newest first.
func (r *Repository) ListMessages(ctx context.Context, f ListFilter) ([]*Message, error) {
	if f.Limit <= 0 || f.Limit > 200 {
		f.Limit = 50
	}
	if f.Offset < 0 {
		f.Offset = 0
	}
	q := "SELECT " + messageColumns + " FROM mail_messages WHERE user_id = ?"
	args := []any{f.UserID}
	if f.MailboxID != "" {
		q += " AND mailbox_id = ?"
		args = append(args, f.MailboxID)
	}
	if f.Direction != "" {
		q += " AND direction = ?"
		args = append(args, string(f.Direction))
	}
	switch f.Folder {
	case "inbox":
		q += " AND direction = 'inbound'"
	case "sent":
		q += " AND direction = 'outbound' AND is_draft = 0"
	case "draft":
		q += " AND is_draft = 1"
	case "starred":
		q += " AND is_starred = 1"
	case "unread":
		q += " AND is_read = 0 AND direction = 'inbound'"
	}
	if f.ThreadID != "" {
		q += " AND thread_id = ?"
		args = append(args, f.ThreadID)
	}
	q += " ORDER BY created_at DESC LIMIT ? OFFSET ?"
	args = append(args, f.Limit, f.Offset)

	rows, err := r.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Message
	for rows.Next() {
		m, err := scanMessage(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// ListThreads returns one summary row per conversation (thread_id) for the
// user, newest activity first. Each row carries the latest message's subject/
// sender/time plus total and unread counts within the thread.
func (r *Repository) ListThreads(ctx context.Context, userID, mailboxID string, limit, offset int) ([]Thread, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	q := `
		SELECT t.thread_id,
		       (SELECT COALESCE(subject,'')  FROM mail_messages WHERE user_id = ? AND thread_id = t.thread_id` + mailboxFilter(mailboxID) + ` ORDER BY created_at DESC LIMIT 1),
		       (SELECT COALESCE(from_addr,'{}') FROM mail_messages WHERE user_id = ? AND thread_id = t.thread_id` + mailboxFilter(mailboxID) + ` ORDER BY created_at DESC LIMIT 1),
		       CAST(strftime('%s', t.last_at) AS INTEGER), t.cnt, t.unread
		FROM (
		  SELECT thread_id,
		         MAX(CASE WHEN direction='inbound' THEN created_at ELSE COALESCE(sent_at, created_at) END) AS last_at,
		         COUNT(*) AS cnt,
		         SUM(CASE WHEN is_read=0 AND direction='inbound' THEN 1 ELSE 0 END) AS unread
		  FROM mail_messages
		  WHERE user_id = ?` + mailboxFilter(mailboxID) + `
		  GROUP BY thread_id
		) t
		ORDER BY t.last_at DESC
		LIMIT ? OFFSET ?`
	args := []any{userID, userID, userID}
	if mailboxID != "" {
		args = []any{userID, mailboxID, userID, mailboxID, userID, mailboxID}
	}
	args = append(args, limit, offset)
	rows, err := r.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("inbox: list threads: %w", err)
	}
	defer rows.Close()
	var out []Thread
	for rows.Next() {
		var th Thread
		var fromJSON string
		var epoch int64
		var cnt, unread int
		if err := rows.Scan(&th.ThreadID, &th.Subject, &fromJSON, &epoch, &cnt, &unread); err != nil {
			return nil, err
		}
		if fa := letter.UnmarshalAddresses(fromJSON); len(fa) > 0 {
			th.From = fa[0]
		}
		if epoch > 0 {
			th.LastAt = time.Unix(epoch, 0).UTC()
		}
		th.Count = cnt
		th.UnreadCount = unread
		out = append(out, th)
	}
	return out, rows.Err()
}

// mailboxFilter returns " AND mailbox_id = ?" fragment placeholder-less; the
// caller threads the matching args. Empty mailboxID means "all mailboxes".
func mailboxFilter(mailboxID string) string {
	if mailboxID == "" {
		return ""
	}
	return " AND mailbox_id = ?"
}

// FlagPatch is a partial update of message flags. nil pointers are untouched.
type FlagPatch struct {
	IsRead    *bool
	IsStarred *bool
	IsDraft   *bool
}

// PatchFlags applies a flag patch.
func (r *Repository) PatchFlags(ctx context.Context, userID, id string, patch FlagPatch) error {
	sets := []string{}
	args := []any{}
	if patch.IsRead != nil {
		sets = append(sets, "is_read = ?")
		args = append(args, boolToInt(*patch.IsRead))
	}
	if patch.IsStarred != nil {
		sets = append(sets, "is_starred = ?")
		args = append(args, boolToInt(*patch.IsStarred))
	}
	if patch.IsDraft != nil {
		sets = append(sets, "is_draft = ?")
		args = append(args, boolToInt(*patch.IsDraft))
	}
	if len(sets) == 0 {
		return nil
	}
	sets = append(sets, "updated_at = ?")
	args = append(args, time.Now().UTC().Truncate(time.Second))
	args = append(args, id, userID)
	res, err := r.db.ExecContext(ctx,
		"UPDATE mail_messages SET "+strings.Join(sets, ", ")+" WHERE id = ? AND user_id = ?", args...)
	if err != nil {
		return fmt.Errorf("inbox: patch flags: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrMessageNotFound
	}
	return nil
}

// DeleteMessage hard-deletes a message.
func (r *Repository) DeleteMessage(ctx context.Context, userID, id string) error {
	res, err := r.db.ExecContext(ctx,
		"DELETE FROM mail_messages WHERE id = ? AND user_id = ?", id, userID)
	if err != nil {
		return fmt.Errorf("inbox: delete message: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrMessageNotFound
	}
	return nil
}

// --- helpers ---------------------------------------------------------------

func (r *Repository) queryOneMailbox(ctx context.Context, query string, args ...any) (*Mailbox, error) {
	row := r.db.QueryRowContext(ctx, query, args...)
	mb, err := scanMailbox(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrMailboxNotFound
	}
	return mb, err
}

type scanner interface {
	Scan(dest ...any) error
}

const messageColumns = `id, mailbox_id, user_id, COALESCE(message_id,''),
	COALESCE(thread_id,''), COALESCE(in_reply_to,''), COALESCE(refs,'[]'),
	COALESCE(subject,''), COALESCE(from_addr,'{}'), COALESCE(to_addrs,'[]'), COALESCE(cc_addrs,'[]'),
	COALESCE(bcc_addrs,'[]'), COALESCE(reply_to_addrs,'[]'), direction, COALESCE(raw_path,''),
	COALESCE(body_text,''), COALESCE(body_html,''),
	is_read, is_starred, is_draft, is_outbox, size_bytes, received_at, sent_at,
	created_at, updated_at`

func scanMailbox(row scanner) (*Mailbox, error) {
	var mb Mailbox
	var primary int
	err := row.Scan(&mb.ID, &mb.UserID, &mb.LocalPart, &mb.Domain, &mb.Address,
		&mb.DisplayName, &primary, &mb.CreatedAt, &mb.UpdatedAt)
	if err != nil {
		return nil, err
	}
	mb.IsPrimary = primary == 1
	return &mb, nil
}

func scanMessage(row scanner) (*Message, error) {
	var m Message
	var dir string
	var read, starred, draft, outbox int
	var fromJSON, refsJSON string
	var toJSON, ccJSON, bccJSON, rtJSON string
	var received, sent sql.NullTime
	err := row.Scan(
		&m.ID, &m.MailboxID, &m.UserID, &m.MessageID, &m.ThreadID, &m.InReplyTo, &refsJSON,
		&m.Subject, &fromJSON, &toJSON, &ccJSON, &bccJSON, &rtJSON, &dir, &m.RawPath,
		&m.BodyText, &m.BodyHTML,
		&read, &starred, &draft, &outbox, &m.SizeBytes, &received, &sent,
		&m.CreatedAt, &m.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	m.Direction = Direction(dir)
	m.IsRead = read == 1
	m.IsStarred = starred == 1
	m.IsDraft = draft == 1
	m.IsOutbox = outbox == 1
	if fa := letter.UnmarshalAddresses(fromJSON); len(fa) > 0 {
		m.From = fa[0]
	}
	m.To = letter.UnmarshalAddresses(toJSON)
	m.Cc = letter.UnmarshalAddresses(ccJSON)
	m.Bcc = letter.UnmarshalAddresses(bccJSON)
	m.ReplyTo = letter.UnmarshalAddresses(rtJSON)
	_ = json.Unmarshal([]byte(refsJSON), &m.References)
	if received.Valid {
		t := received.Time.UTC()
		m.ReceivedAt = &t
	}
	if sent.Valid {
		t := sent.Time.UTC()
		m.SentAt = &t
	}
	return &m, nil
}

func nextID() string { return id.New() }

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func nullableTime(t *time.Time) any {
	if t == nil {
		return nil
	}
	return *t
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// isUniqueViolation reports whether err is a SQLite UNIQUE constraint failure.
// It checks specifically for "UNIQUE" so a FOREIGN KEY or CHECK constraint
// failure is not mistaken for a duplicate. modernc.org/sqlite formats these as
// "constraint failed: UNIQUE constraint failed: <table>.<col> ...".
func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "UNIQUE constraint failed")
}
