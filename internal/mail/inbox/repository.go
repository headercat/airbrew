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
		tid := letter.ThreadKey(m.MessageID, m.InReplyTo, m.References)
		// Messages without a Message-ID and without any References chain
		// (cron output, some automated mail, headerless spam) all hash to
		// the literal "no-id"; if we stored that they would collapse into a
		// single fake thread. Fall back to the message's own row id so each
		// headerless message starts its own singleton conversation.
		if tid == "" || tid == "no-id" {
			tid = m.ID
		}
		m.ThreadID = tid
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
	Query     string // free-text search across subject/from/to/body
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
	where, args := buildListWhere(f)
	q := "SELECT " + messageColumns + " FROM mail_messages " + where +
		" ORDER BY created_at DESC LIMIT ? OFFSET ?"
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

// CountMessages returns the total number of messages matching the filter,
// ignoring Limit/Offset. Use it to render "1–50 of N" and decide whether to
// offer "load more" in the SPA.
func (r *Repository) CountMessages(ctx context.Context, f ListFilter) (int, error) {
	where, args := buildListWhere(f)
	q := "SELECT COUNT(*) FROM mail_messages " + where
	var count int
	if err := r.db.QueryRowContext(ctx, q, args...).Scan(&count); err != nil {
		return 0, err
	}
	return count, nil
}

// buildListWhere assembles the WHERE clause + args shared by ListMessages and
// CountMessages so the two queries never drift apart.
func buildListWhere(f ListFilter) (string, []any) {
	q := "WHERE user_id = ?"
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
		q += " AND direction = 'outbound' AND is_draft = 0 AND is_outbox = 0"
	case "draft":
		q += " AND is_draft = 1"
	case "starred":
		q += " AND is_starred = 1"
	case "unread":
		q += " AND is_read = 0 AND direction = 'inbound'"
	case "outbox":
		q += " AND is_outbox = 1"
	}
	if f.ThreadID != "" {
		q += " AND thread_id = ?"
		args = append(args, f.ThreadID)
	}
	q, args = applySearch(q, args, f.Query)
	return q, args
}

// applySearch appends a case-insensitive LIKE clause across subject, from, to
// and body_text when query is non-empty. It returns the augmented SQL fragment
// and args slice.
func applySearch(q string, args []any, query string) (string, []any) {
	query = strings.TrimSpace(query)
	if query == "" {
		return q, args
	}
	// Escape LIKE wildcards in the user input so a literal "%" or "_" in the
	// search string does not act as a pattern.
	escaped := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(query)
	like := "%" + escaped + "%"
	// body_html is included so legacy HTML-only rows (stored before ingest
	// began synthesising a text fallback) are still searchable.
	q += " AND (subject LIKE ? ESCAPE '\\' OR from_addr LIKE ? ESCAPE '\\' OR to_addrs LIKE ? ESCAPE '\\' OR cc_addrs LIKE ? ESCAPE '\\' OR body_text LIKE ? ESCAPE '\\' OR body_html LIKE ? ESCAPE '\\')"
	args = append(args, like, like, like, like, like, like)
	return q, args
}

// Counts returns per-folder totals for the user (optionally scoped to one
// mailbox). A single query scans the user's messages so the SPA sidebar can
// render unread/draft/starred badges without N round-trips.
func (r *Repository) Counts(ctx context.Context, userID, mailboxID string) (FolderCounts, error) {
	q := `
		SELECT
		  SUM(CASE WHEN direction='inbound'                                  THEN 1 ELSE 0 END),
		  SUM(CASE WHEN direction='outbound' AND is_draft = 0 AND is_outbox = 0 THEN 1 ELSE 0 END),
		  SUM(CASE WHEN is_draft = 1                                          THEN 1 ELSE 0 END),
		  SUM(CASE WHEN is_starred = 1                                        THEN 1 ELSE 0 END),
		  SUM(CASE WHEN is_read = 0 AND direction = 'inbound'                 THEN 1 ELSE 0 END),
		  SUM(CASE WHEN is_outbox = 1                                         THEN 1 ELSE 0 END)
		FROM mail_messages WHERE user_id = ?`
	args := []any{userID}
	if mailboxID != "" {
		q += " AND mailbox_id = ?"
		args = append(args, mailboxID)
	}
	var c FolderCounts
	var inbox, sent, draft, starred, unread, outbox sql.NullInt64
	if err := r.db.QueryRowContext(ctx, q, args...).Scan(
		&inbox, &sent, &draft, &starred, &unread, &outbox,
	); err != nil {
		return FolderCounts{}, fmt.Errorf("inbox: counts: %w", err)
	}
	c.Inbox = int(inbox.Int64)
	c.Sent = int(sent.Int64)
	c.Draft = int(draft.Int64)
	c.Starred = int(starred.Int64)
	c.Unread = int(unread.Int64)
	c.Outbox = int(outbox.Int64)
	return c, nil
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

// --- attachments ----------------------------------------------------------

const attachmentColumns = `id, COALESCE(message_id,''), user_id, blob_path,
	COALESCE(filename,''), COALESCE(content_type,'application/octet-stream'),
	COALESCE(content_id,''), disposition, size_bytes, created_at`

// CreateAttachment inserts an attachment row. MessageID may be empty for a
// pending outbound upload.
func (r *Repository) CreateAttachment(ctx context.Context, a *Attachment) error {
	now := time.Now().UTC().Truncate(time.Second)
	a.CreatedAt = now
	disp := "attachment"
	if a.Inline {
		disp = "inline"
	}
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO mail_attachments
		  (id, message_id, user_id, blob_path, filename, content_type,
		   content_id, disposition, size_bytes, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		a.ID, nullable(a.MessageID), a.UserID, a.BlobPath, a.Filename, a.ContentType,
		nullable(a.ContentID), disp, a.SizeBytes, now,
	)
	if err != nil {
		return fmt.Errorf("inbox: insert attachment: %w", err)
	}
	return nil
}

// ListAttachmentsByMessage returns the attachments attached to a message.
func (r *Repository) ListAttachmentsByMessage(ctx context.Context, userID, messageID string) ([]Attachment, error) {
	rows, err := r.db.QueryContext(ctx,
		"SELECT "+attachmentColumns+" FROM mail_attachments WHERE user_id = ? AND message_id = ? ORDER BY created_at ASC",
		userID, messageID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanAttachments(rows)
}

// ListByIDs returns the attachments owned by userID matching the given ids,
// including pending (message_id NULL) rows.
func (r *Repository) ListByIDs(ctx context.Context, userID string, ids []string) ([]Attachment, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	placeholders := strings.Repeat("?,", len(ids))
	placeholders = placeholders[:len(placeholders)-1]
	args := []any{userID}
	for _, id := range ids {
		args = append(args, id)
	}
	rows, err := r.db.QueryContext(ctx,
		"SELECT "+attachmentColumns+" FROM mail_attachments WHERE user_id = ? AND id IN ("+placeholders+")",
		args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanAttachments(rows)
}

// GetAttachment returns one attachment (ownership-scoped).
func (r *Repository) GetAttachment(ctx context.Context, userID, id string) (Attachment, error) {
	row := r.db.QueryRowContext(ctx,
		"SELECT "+attachmentColumns+" FROM mail_attachments WHERE user_id = ? AND id = ?", userID, id)
	out, err := scanAttachment(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Attachment{}, ErrMessageNotFound
	}
	return out, err
}

// LinkAttachments sets message_id on the given attachment ids (ownership-scoped)
// so pending uploads become attached to the sent message. Only pending rows
// (message_id IS NULL) are touched; attachments already bound to a previously
// sent or received message cannot be silently migrated away from it.
func (r *Repository) LinkAttachments(ctx context.Context, userID, messageID string, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	placeholders := strings.Repeat("?,", len(ids))
	placeholders = placeholders[:len(placeholders)-1]
	args := []any{messageID, userID}
	for _, id := range ids {
		args = append(args, id)
	}
	_, err := r.db.ExecContext(ctx,
		"UPDATE mail_attachments SET message_id = ? WHERE user_id = ? AND id IN ("+placeholders+") AND message_id IS NULL",
		args...)
	if err != nil {
		return fmt.Errorf("inbox: link attachments: %w", err)
	}
	return nil
}

// DeleteAttachment removes an attachment row (ownership-scoped).
func (r *Repository) DeleteAttachment(ctx context.Context, userID, id string) error {
	res, err := r.db.ExecContext(ctx,
		"DELETE FROM mail_attachments WHERE user_id = ? AND id = ?", userID, id)
	if err != nil {
		return fmt.Errorf("inbox: delete attachment: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrMessageNotFound
	}
	return nil
}

func scanAttachments(rows *sql.Rows) ([]Attachment, error) {
	var out []Attachment
	for rows.Next() {
		a, err := scanAttachment(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func scanAttachment(row scanner) (Attachment, error) {
	var a Attachment
	var disp string
	err := row.Scan(&a.ID, &a.MessageID, &a.UserID, &a.BlobPath, &a.Filename,
		&a.ContentType, &a.ContentID, &disp, &a.SizeBytes, &a.CreatedAt)
	if err != nil {
		return Attachment{}, err
	}
	a.Inline = disp == "inline"
	return a, nil
}

// FlagPatch is a partial update of message flags. nil pointers are untouched.
type FlagPatch struct {
	IsRead    *bool
	IsStarred *bool
	IsDraft   *bool
}

// MarkSent flips is_outbox off and stamps sent_at, called by Send after a
// successful delivery. The WHERE clause requires is_outbox=1 so two
// concurrent retries cannot both claim the transition; the loser's
// RowsAffected is 0 and the call returns nil (the message is already
// delivered, no duplicate should be produced). Errors from this call should
// be treated as best-effort by the caller — the driver has already accepted
// the message.
func (r *Repository) MarkSent(ctx context.Context, userID, id string, sentAt time.Time) error {
	now := time.Now().UTC().Truncate(time.Second)
	_, err := r.db.ExecContext(ctx, `
		UPDATE mail_messages SET is_outbox = 0, sent_at = ?, updated_at = ?
		WHERE id = ? AND user_id = ? AND is_outbox = 1`,
		sentAt, now, id, userID,
	)
	if err != nil {
		return fmt.Errorf("inbox: mark sent: %w", err)
	}
	return nil
}

// MarkOutbox flips a draft into the outbox (is_draft=0, is_outbox=1), used
// by SendDraft right before invoking the outbound driver. Combined with the
// pre-written Message-ID, raw blob and size, this means a crash or DB outage
// between send and persist leaves the row recoverable via RetrySend (and the
// Message-ID is stable, so retries never produce duplicates).
func (r *Repository) MarkOutbox(ctx context.Context, userID, id string) error {
	now := time.Now().UTC().Truncate(time.Second)
	res, err := r.db.ExecContext(ctx, `
		UPDATE mail_messages SET is_draft = 0, is_outbox = 1, updated_at = ?
		WHERE id = ? AND user_id = ? AND is_draft = 1`,
		now, id, userID,
	)
	if err != nil {
		return fmt.Errorf("inbox: mark outbox: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrMessageNotFound
	}
	return nil
}

// OutboxItem is the lightweight row the outbox sweeper needs: who owns the
// message, which row to retry, and how many attempts have already run (so the
// backoff schedule can advance). It avoids pulling the full message body.
type OutboxItem struct {
	UserID   string
	ID       string
	Attempts int
}

// ListOutboxDue returns up to limit outbox rows whose backoff window has
// elapsed and that have not exceeded maxAttempts. It is user-agnostic because
// the background sweeper retries on behalf of every user.
func (r *Repository) ListOutboxDue(ctx context.Context, now time.Time, maxAttempts int, limit int) ([]OutboxItem, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := r.db.QueryContext(ctx, `
		SELECT user_id, id, outbox_attempts
		FROM mail_messages
		WHERE is_outbox = 1
		  AND outbox_attempts < ?
		  AND (outbox_next_attempt IS NULL OR outbox_next_attempt <= ?)
		ORDER BY outbox_next_attempt ASC
		LIMIT ?`,
		maxAttempts, now.UTC().Truncate(time.Second), limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []OutboxItem
	for rows.Next() {
		var it OutboxItem
		if err := rows.Scan(&it.UserID, &it.ID, &it.Attempts); err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

// RecordOutboxAttempt increments the attempt counter for id and schedules the
// next retry at nextAttempt. Called after a failed retry so the sweeper backs
// off rather than hammering a failing provider.
func (r *Repository) RecordOutboxAttempt(ctx context.Context, id string, nextAttempt time.Time) error {
	now := time.Now().UTC().Truncate(time.Second)
	_, err := r.db.ExecContext(ctx, `
		UPDATE mail_messages
		SET outbox_attempts = outbox_attempts + 1,
		    outbox_next_attempt = ?,
		    updated_at = ?
		WHERE id = ? AND is_outbox = 1`,
		nextAttempt.UTC().Truncate(time.Second), now, id,
	)
	if err != nil {
		return fmt.Errorf("inbox: record outbox attempt: %w", err)
	}
	return nil
}

// ResetOutboxAttempts clears the attempt counter and, only when no other
// worker holds the row, the next-attempt lease. Used by the manual retry
// path so a user-initiated retry resumes the automatic sweeper even after
// the row previously hit the attempt cap. The lease guard is critical: an
// in-flight sweeper retry holds outbox_next_attempt in the future; clearing
// it unconditionally would let the manual retry's ClaimOutbox succeed while
// the sweeper is still mid-send → duplicate delivery.
func (r *Repository) ResetOutboxAttempts(ctx context.Context, id string) error {
	now := time.Now().UTC().Truncate(time.Second)
	_, err := r.db.ExecContext(ctx, `
		UPDATE mail_messages
		SET outbox_attempts = 0,
		    outbox_next_attempt = CASE
			    WHEN outbox_next_attempt IS NULL OR outbox_next_attempt <= ? THEN NULL
			    ELSE outbox_next_attempt
		    END,
		    updated_at = ?
		WHERE id = ? AND is_outbox = 1`, now, now, id)
	if err != nil {
		return fmt.Errorf("inbox: reset outbox attempts: %w", err)
	}
	return nil
}

// ClaimOutbox atomically reserves an outbox row for a retry attempt by moving
// its next_attempt into the future. It returns true when this caller won the
// claim. The sweeper and the manual-retry HTTP path both go through RetrySend,
// so without this guard the two can pick the same row concurrently and both
// hand it to the driver — producing a duplicate real-world delivery. The claim
// is released by MarkSent (on success) or RecordOutboxAttempt (on failure).
func (r *Repository) ClaimOutbox(ctx context.Context, id string, lease time.Duration) (bool, error) {
	now := time.Now().UTC().Truncate(time.Second)
	holdUntil := now.Add(lease)
	res, err := r.db.ExecContext(ctx, `
		UPDATE mail_messages
		SET outbox_next_attempt = ?, updated_at = ?
		WHERE id = ? AND is_outbox = 1
		  AND (outbox_next_attempt IS NULL OR outbox_next_attempt <= ?)`,
		holdUntil, now, id, now)
	if err != nil {
		return false, fmt.Errorf("inbox: claim outbox: %w", err)
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
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

// UpdateDraft writes the editable fields of an existing draft (subject, bodies,
// addresses, references, raw path, size, draft/sent flags). It is ownership-
// scoped and rejects non-draft messages upstream in the service layer.
func (r *Repository) UpdateDraft(ctx context.Context, m *Message) error {
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
		tid := letter.ThreadKey(m.MessageID, m.InReplyTo, m.References)
		if tid == "" || tid == "no-id" {
			tid = m.ID
		}
		m.ThreadID = tid
	}
	now := time.Now().UTC().Truncate(time.Second)
	m.UpdatedAt = now
	res, err := r.db.ExecContext(ctx, `
		UPDATE mail_messages SET
		  mailbox_id = ?, message_id = ?, thread_id = ?, in_reply_to = ?, refs = ?,
		  subject = ?, from_addr = ?, to_addrs = ?, cc_addrs = ?, bcc_addrs = ?,
		  reply_to_addrs = ?, raw_path = ?, body_text = ?, body_html = ?,
		  is_draft = ?, is_outbox = ?, size_bytes = ?,
		  sent_at = ?, updated_at = ?
		WHERE id = ? AND user_id = ? AND is_draft = 1
	`,
		m.MailboxID, nullable(m.MessageID), nullable(m.ThreadID), nullable(m.InReplyTo), string(refs),
		nullable(m.Subject), from, to, cc, bcc, rt,
		nullable(m.RawPath), nullable(m.BodyText), nullable(m.BodyHTML),
		boolToInt(m.IsDraft), boolToInt(m.IsOutbox), m.SizeBytes,
		nullableTime(m.SentAt), now,
		m.ID, m.UserID,
	)
	if err != nil {
		return fmt.Errorf("inbox: update draft: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrMessageNotFound
	}
	return nil
}

// RebindAttachments replaces the attachment set bound to a draft. Pending
// attachments (message_id NULL) listed in keepIDs are linked; attachments that
// were previously linked to this draft but are no longer in keepIDs are
// unlinked back to the pending pool so they remain owned by the user until
// explicit cleanup.
func (r *Repository) RebindAttachments(ctx context.Context, userID, messageID string, keepIDs []string) error {
	if _, err := r.db.ExecContext(ctx,
		"UPDATE mail_attachments SET message_id = NULL WHERE user_id = ? AND message_id = ?",
		userID, messageID,
	); err != nil {
		return fmt.Errorf("inbox: clear draft attachments: %w", err)
	}
	return r.LinkAttachments(ctx, userID, messageID, keepIDs)
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
