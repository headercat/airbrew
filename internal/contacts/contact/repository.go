package contact

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/headercat/airbrew/internal/id"
)

// Repository persists contacts, groups and their membership.
type Repository struct {
	db *sql.DB
}

// NewRepository returns a Repository bound to db.
func NewRepository(db *sql.DB) *Repository { return &Repository{db: db} }

const contactColumns = `id, user_id, COALESCE(uid,''),
	COALESCE(name_prefix,''), COALESCE(given_name,''), COALESCE(middle_name,''),
	COALESCE(family_name,''), COALESCE(name_suffix,''), COALESCE(display_name,''),
	COALESCE(nickname,''), COALESCE(company,''), COALESCE(title,''),
	COALESCE(department,''), emails, phones, addresses, ims, urls,
	birthday, COALESCE(notes,''), COALESCE(avatar_path,''), is_favorite,
	created_at, updated_at`

// CreateWithGroups inserts a contact and assigns its group memberships in one
// transaction, so a failure rolls back the contact row too. groupIDs == nil
// means "no membership change" (still inserts the contact).
func (r *Repository) CreateWithGroups(ctx context.Context, c *Contact, groupIDs []string) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("contacts: begin create tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }() //nolint:errcheck
	if err := insertContactTx(ctx, tx, c); err != nil {
		return err
	}
	if groupIDs != nil {
		if err := applyMembershipTx(ctx, tx, c.UserID, c.ID, groupIDs); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("contacts: commit create: %w", err)
	}
	return nil
}

// execer is the shared subset of *sql.DB and *sql.Tx used by the tx-aware
// helpers, so the same code path serves both standalone and transactional use.
type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func insertContactTx(ctx context.Context, q execer, c *Contact) error {
	now := time.Now().UTC().Truncate(time.Second)
	c.CreatedAt = now
	c.UpdatedAt = now
	if _, err := q.ExecContext(ctx, `
		INSERT INTO contacts
		  (id, user_id, uid, name_prefix, given_name, middle_name, family_name,
		   name_suffix, display_name, nickname, company, title, department,
		   emails, phones, addresses, ims, urls, birthday, notes, avatar_path,
		   is_favorite, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		c.ID, c.UserID, nullable(c.UID), c.NamePrefix, c.GivenName, c.MiddleName, c.FamilyName,
		c.NameSuffix, c.DisplayName, c.Nickname, c.Company, c.Title, c.Department,
		marshalJSON(c.Emails), marshalJSON(c.Phones), marshalJSON(c.Addresses),
		marshalJSON(c.IMs), marshalJSON(c.URLs), nullableTime(c.Birthday), c.Notes,
		nullable(c.AvatarPath), boolToInt(c.IsFavorite), now, now,
	); err != nil {
		return fmt.Errorf("contacts: insert contact: %w", err)
	}
	return nil
}

// GetContact returns one contact owned by userID.
func (r *Repository) GetContact(ctx context.Context, userID, id string) (*Contact, error) {
	row := r.db.QueryRowContext(ctx,
		"SELECT "+contactColumns+" FROM contacts WHERE id = ? AND user_id = ?", id, userID)
	c, err := scanContact(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return c, err
}

// GetContactByUID returns one contact matching the stable external uid, for
// import dedup. An empty uid always returns ErrNotFound.
func (r *Repository) GetContactByUID(ctx context.Context, userID, uid string) (*Contact, error) {
	if uid == "" {
		return nil, ErrNotFound
	}
	row := r.db.QueryRowContext(ctx,
		"SELECT "+contactColumns+" FROM contacts WHERE user_id = ? AND uid = ?", userID, uid)
	c, err := scanContact(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return c, err
}

// ListContacts returns contacts matching the filter, sorted.
func (r *Repository) ListContacts(ctx context.Context, f ListFilter) ([]*Contact, error) {
	maxLimit := 200
	if f.MaxLimit > 0 {
		maxLimit = f.MaxLimit
	}
	if f.Limit <= 0 {
		f.Limit = 100
	}
	if f.Limit > maxLimit {
		f.Limit = maxLimit
	}
	if f.Offset < 0 {
		f.Offset = 0
	}
	q := "SELECT " + contactColumns + " FROM contacts WHERE user_id = ?"
	args := []any{f.UserID}
	if f.Favorite {
		q += " AND is_favorite = 1"
	}
	if s := strings.TrimSpace(f.Search); s != "" {
		q += " AND (display_name LIKE ? ESCAPE '\\' OR given_name LIKE ? ESCAPE '\\' OR family_name LIKE ? ESCAPE '\\' OR nickname LIKE ? ESCAPE '\\' OR company LIKE ? ESCAPE '\\' OR emails LIKE ? ESCAPE '\\' OR phones LIKE ? ESCAPE '\\' OR notes LIKE ? ESCAPE '\\')"
		like := "%" + likeEscape(s) + "%"
		for i := 0; i < 8; i++ {
			args = append(args, like)
		}
	}
	if f.GroupID != "" {
		q += " AND id IN (SELECT contact_id FROM contact_group_members WHERE group_id = ?)"
		args = append(args, f.GroupID)
	}
	q += " ORDER BY " + sortClause(f.SortBy, f.SortDesc)
	q += " LIMIT ? OFFSET ?"
	args = append(args, f.Limit, f.Offset)

	rows, err := r.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Contact
	for rows.Next() {
		c, err := scanContact(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// CountContacts returns the number of contacts owned by userID.
func (r *Repository) CountContacts(ctx context.Context, userID string) (int, error) {
	var n int
	if err := r.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM contacts WHERE user_id = ?", userID).Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

// CountContactsFiltered returns the total number of contacts matching the same
// filter used by ListContacts (ignoring limit/offset/sort), so the UI can show
// page counts and "load more" affordances.
func (r *Repository) CountContactsFiltered(ctx context.Context, f ListFilter) (int, error) {
	q := "SELECT COUNT(*) FROM contacts WHERE user_id = ?"
	args := []any{f.UserID}
	if f.Favorite {
		q += " AND is_favorite = 1"
	}
	if s := strings.TrimSpace(f.Search); s != "" {
		q += " AND (display_name LIKE ? ESCAPE '\\' OR given_name LIKE ? ESCAPE '\\' OR family_name LIKE ? ESCAPE '\\' OR nickname LIKE ? ESCAPE '\\' OR company LIKE ? ESCAPE '\\' OR emails LIKE ? ESCAPE '\\' OR phones LIKE ? ESCAPE '\\' OR notes LIKE ? ESCAPE '\\')"
		like := "%" + likeEscape(s) + "%"
		for i := 0; i < 8; i++ {
			args = append(args, like)
		}
	}
	if f.GroupID != "" {
		q += " AND id IN (SELECT contact_id FROM contact_group_members WHERE group_id = ?)"
		args = append(args, f.GroupID)
	}
	var n int
	if err := r.db.QueryRowContext(ctx, q, args...).Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

// UpdateContact replaces every editable field on the contact (full PUT).
func (r *Repository) UpdateContact(ctx context.Context, userID, id string, in UpdateContactInput) error {
	n, err := updateContactTx(ctx, r.db, userID, id, in)
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// ReplaceWithGroups updates a contact and replaces its group memberships in one
// transaction. groupIDs == nil means "leave membership unchanged".
func (r *Repository) ReplaceWithGroups(ctx context.Context, userID, id string, in UpdateContactInput, groupIDs []string) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("contacts: begin replace tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }() //nolint:errcheck
	n, err := updateContactTx(ctx, tx, userID, id, in)
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	if groupIDs != nil {
		if err := applyMembershipTx(ctx, tx, userID, id, groupIDs); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("contacts: commit replace: %w", err)
	}
	return nil
}

// updateContactTx runs the full PUT update against q (db or tx) and returns the
// number of rows affected.
func updateContactTx(ctx context.Context, q execer, userID, id string, in UpdateContactInput) (int64, error) {
	now := time.Now().UTC().Truncate(time.Second)
	res, err := q.ExecContext(ctx, `
		UPDATE contacts SET
		  name_prefix = ?, given_name = ?, middle_name = ?, family_name = ?,
		  name_suffix = ?, display_name = ?, nickname = ?, company = ?, title = ?,
		  department = ?, emails = ?, phones = ?, addresses = ?, ims = ?, urls = ?,
		  birthday = ?, notes = ?, is_favorite = ?, updated_at = ?
		WHERE id = ? AND user_id = ?
	`,
		in.NamePrefix, in.GivenName, in.MiddleName, in.FamilyName, in.NameSuffix,
		in.DisplayName, in.Nickname, in.Company, in.Title, in.Department,
		marshalJSON(in.Emails), marshalJSON(in.Phones), marshalJSON(in.Addresses),
		marshalJSON(in.IMs), marshalJSON(in.URLs), nullableTime(in.Birthday), in.Notes,
		boolToInt(in.IsFavorite), now, id, userID,
	)
	if err != nil {
		return 0, fmt.Errorf("contacts: update contact: %w", err)
	}
	return res.RowsAffected()
}

// PatchContact applies a partial update. Slice pointers replace the whole
// slice when non-nil; BirthdaySet controls whether Birthday is written.
func (r *Repository) PatchContact(ctx context.Context, userID, id string, p ContactPatch) error {
	sets := []string{}
	args := []any{}
	if p.NamePrefix != nil {
		sets = append(sets, "name_prefix = ?")
		args = append(args, *p.NamePrefix)
	}
	if p.GivenName != nil {
		sets = append(sets, "given_name = ?")
		args = append(args, *p.GivenName)
	}
	if p.MiddleName != nil {
		sets = append(sets, "middle_name = ?")
		args = append(args, *p.MiddleName)
	}
	if p.FamilyName != nil {
		sets = append(sets, "family_name = ?")
		args = append(args, *p.FamilyName)
	}
	if p.NameSuffix != nil {
		sets = append(sets, "name_suffix = ?")
		args = append(args, *p.NameSuffix)
	}
	if p.DisplayName != nil {
		sets = append(sets, "display_name = ?")
		args = append(args, *p.DisplayName)
	}
	if p.Nickname != nil {
		sets = append(sets, "nickname = ?")
		args = append(args, *p.Nickname)
	}
	if p.Company != nil {
		sets = append(sets, "company = ?")
		args = append(args, *p.Company)
	}
	if p.Title != nil {
		sets = append(sets, "title = ?")
		args = append(args, *p.Title)
	}
	if p.Department != nil {
		sets = append(sets, "department = ?")
		args = append(args, *p.Department)
	}
	if p.Emails != nil {
		sets = append(sets, "emails = ?")
		args = append(args, marshalJSON(*p.Emails))
	}
	if p.Phones != nil {
		sets = append(sets, "phones = ?")
		args = append(args, marshalJSON(*p.Phones))
	}
	if p.Addresses != nil {
		sets = append(sets, "addresses = ?")
		args = append(args, marshalJSON(*p.Addresses))
	}
	if p.IMs != nil {
		sets = append(sets, "ims = ?")
		args = append(args, marshalJSON(*p.IMs))
	}
	if p.URLs != nil {
		sets = append(sets, "urls = ?")
		args = append(args, marshalJSON(*p.URLs))
	}
	if p.BirthdaySet {
		sets = append(sets, "birthday = ?")
		args = append(args, nullableTime(p.Birthday))
	}
	if p.Notes != nil {
		sets = append(sets, "notes = ?")
		args = append(args, *p.Notes)
	}
	if p.IsFavorite != nil {
		sets = append(sets, "is_favorite = ?")
		args = append(args, boolToInt(*p.IsFavorite))
	}
	if len(sets) == 0 {
		return nil
	}
	sets = append(sets, "updated_at = ?")
	args = append(args, time.Now().UTC().Truncate(time.Second), id, userID)
	res, err := r.db.ExecContext(ctx,
		"UPDATE contacts SET "+strings.Join(sets, ", ")+" WHERE id = ? AND user_id = ?", args...)
	if err != nil {
		return fmt.Errorf("contacts: patch contact: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// SetAvatar records the blob path for a contact's avatar. An empty avatarPath
// clears the stored path (sets it to NULL) without touching the blob store.
func (r *Repository) SetAvatar(ctx context.Context, userID, id, avatarPath string) error {
	res, err := r.db.ExecContext(ctx,
		"UPDATE contacts SET avatar_path = ?, updated_at = ? WHERE id = ? AND user_id = ?",
		nullable(avatarPath), time.Now().UTC().Truncate(time.Second), id, userID)
	if err != nil {
		return fmt.Errorf("contacts: set avatar: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteContact removes a contact. Returns its prior avatar path ("" if none)
// so the caller can purge the blob after commit.
func (r *Repository) DeleteContact(ctx context.Context, userID, id string) (string, error) {
	var avatar sql.NullString
	err := r.db.QueryRowContext(ctx,
		"SELECT avatar_path FROM contacts WHERE id = ? AND user_id = ?", id, userID).Scan(&avatar)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	if _, err := r.db.ExecContext(ctx,
		"DELETE FROM contacts WHERE id = ? AND user_id = ?", id, userID); err != nil {
		return "", fmt.Errorf("contacts: delete contact: %w", err)
	}
	if avatar.Valid {
		return avatar.String, nil
	}
	return "", nil
}

// SetContactGroups replaces the full set of group memberships for a contact.
// Passing nil groups leaves membership unchanged; an empty slice clears it.
func (r *Repository) SetContactGroups(ctx context.Context, userID, contactID string, groupIDs []string) error {
	if groupIDs == nil {
		return nil
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("contacts: begin groups tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }() //nolint:errcheck
	if err := applyMembershipTx(ctx, tx, userID, contactID, groupIDs); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("contacts: commit groups: %w", err)
	}
	return nil
}

// applyMembershipTx verifies the contact exists (and is owned by userID),
// clears its current memberships, and inserts the new set. Unknown or
// non-owned group ids are silently skipped so a stale client cannot create
// phantom rows. Runs against q (db or tx) so callers can include it in a
// larger transaction.
func applyMembershipTx(ctx context.Context, q execer, userID, contactID string, groupIDs []string) error {
	var exists int
	if err := q.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM contacts WHERE id = ? AND user_id = ?", contactID, userID).Scan(&exists); err != nil {
		return err
	}
	if exists == 0 {
		return ErrNotFound
	}
	if _, err := q.ExecContext(ctx,
		"DELETE FROM contact_group_members WHERE contact_id = ?", contactID); err != nil {
		return fmt.Errorf("contacts: clear groups: %w", err)
	}
	for _, gid := range groupIDs {
		var owned int
		if err := q.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM contact_groups WHERE id = ? AND user_id = ?", gid, userID).Scan(&owned); err != nil {
			return err
		}
		if owned == 0 {
			continue
		}
		if _, err := q.ExecContext(ctx,
			"INSERT OR IGNORE INTO contact_group_members (contact_id, group_id) VALUES (?, ?)", contactID, gid); err != nil {
			return fmt.Errorf("contacts: insert member: %w", err)
		}
	}
	return nil
}

// ContactGroupIDs returns the ids of the groups a contact belongs to.
func (r *Repository) ContactGroupIDs(ctx context.Context, userID, contactID string) ([]string, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT m.group_id FROM contact_group_members m
		JOIN contact_groups g ON g.id = m.group_id
		WHERE g.user_id = ? AND m.contact_id = ?
		ORDER BY g.name COLLATE NOCASE`, userID, contactID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var gid string
		if err := rows.Scan(&gid); err != nil {
			return nil, err
		}
		out = append(out, gid)
	}
	return out, rows.Err()
}

// GroupIDsForContacts returns a contact-id -> []group-id map for the given
// contacts, in one query, so listings can show group badges without N+1.
func (r *Repository) GroupIDsForContacts(ctx context.Context, userID string, contactIDs []string) (map[string][]string, error) {
	out := map[string][]string{}
	if len(contactIDs) == 0 {
		return out, nil
	}
	placeholders := strings.TrimRight(strings.Repeat("?,", len(contactIDs)), ",")
	args := make([]any, 0, len(contactIDs)+1)
	args = append(args, userID)
	for _, id := range contactIDs {
		args = append(args, id)
	}
	rows, err := r.db.QueryContext(ctx, `
		SELECT m.contact_id, m.group_id FROM contact_group_members m
		JOIN contact_groups g ON g.id = m.group_id
		WHERE g.user_id = ? AND m.contact_id IN (`+placeholders+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var cid, gid string
		if err := rows.Scan(&cid, &gid); err != nil {
			return nil, err
		}
		out[cid] = append(out[cid], gid)
	}
	return out, rows.Err()
}

// --- groups ----------------------------------------------------------------

// CreateGroup inserts a group row.
func (r *Repository) CreateGroup(ctx context.Context, g *Group) error {
	now := time.Now().UTC().Truncate(time.Second)
	g.CreatedAt = now
	g.UpdatedAt = now
	if _, err := r.db.ExecContext(ctx, `
		INSERT INTO contact_groups (id, user_id, name, color, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)
	`, g.ID, g.UserID, g.Name, nullable(g.Color), now, now); err != nil {
		if isUniqueViolation(err) {
			return ErrGroupNameTaken
		}
		return fmt.Errorf("contacts: insert group: %w", err)
	}
	return nil
}

// GetGroup returns one group owned by userID with its member count.
func (r *Repository) GetGroup(ctx context.Context, userID, id string) (*Group, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT g.id, g.user_id, g.name, COALESCE(g.color,''), g.created_at, g.updated_at,
		  (SELECT COUNT(*) FROM contact_group_members m WHERE m.group_id = g.id)
		FROM contact_groups g WHERE g.id = ? AND g.user_id = ?`, id, userID)
	g, err := scanGroup(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrGroupNotFound
	}
	return g, err
}

// ListGroups returns the groups owned by userID with their member counts.
func (r *Repository) ListGroups(ctx context.Context, userID string) ([]*Group, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT g.id, g.user_id, g.name, COALESCE(g.color,''), g.created_at, g.updated_at,
		  (SELECT COUNT(*) FROM contact_group_members m WHERE m.group_id = g.id)
		FROM contact_groups g WHERE g.user_id = ?
		ORDER BY g.name COLLATE NOCASE`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Group
	for rows.Next() {
		g, err := scanGroup(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// UpdateGroup renames / recolors a group.
func (r *Repository) UpdateGroup(ctx context.Context, userID, id, name, color string) error {
	res, err := r.db.ExecContext(ctx,
		"UPDATE contact_groups SET name = ?, color = ?, updated_at = ? WHERE id = ? AND user_id = ?",
		name, nullable(color), time.Now().UTC().Truncate(time.Second), id, userID)
	if err != nil {
		if isUniqueViolation(err) {
			return ErrGroupNameTaken
		}
		return fmt.Errorf("contacts: update group: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrGroupNotFound
	}
	return nil
}

// DeleteGroup removes a group; membership rows cascade.
func (r *Repository) DeleteGroup(ctx context.Context, userID, id string) error {
	res, err := r.db.ExecContext(ctx,
		"DELETE FROM contact_groups WHERE id = ? AND user_id = ?", id, userID)
	if err != nil {
		return fmt.Errorf("contacts: delete group: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrGroupNotFound
	}
	return nil
}

// AllAvatarPathsAll returns every non-empty avatar_path across all users, for
// the janitor's orphan sweep.
func (r *Repository) AllAvatarPathsAll(ctx context.Context) ([]string, error) {
	rows, err := r.db.QueryContext(ctx,
		"SELECT COALESCE(avatar_path,'') FROM contacts WHERE avatar_path IS NOT NULL")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		if p != "" {
			out = append(out, p)
		}
	}
	return out, rows.Err()
}

// --- helpers ---------------------------------------------------------------

type scanner interface {
	Scan(dest ...any) error
}

func scanContact(row scanner) (*Contact, error) {
	var c Contact
	var emails, phones, addresses, ims, urls string
	var favorite int
	var birthday sql.NullTime
	err := row.Scan(&c.ID, &c.UserID, &c.UID,
		&c.NamePrefix, &c.GivenName, &c.MiddleName, &c.FamilyName, &c.NameSuffix,
		&c.DisplayName, &c.Nickname, &c.Company, &c.Title, &c.Department,
		&emails, &phones, &addresses, &ims, &urls,
		&birthday, &c.Notes, &c.AvatarPath, &favorite, &c.CreatedAt, &c.UpdatedAt)
	if err != nil {
		return nil, err
	}
	c.Emails = unmarshalEmails(emails)
	c.Phones = unmarshalPhones(phones)
	c.Addresses = unmarshalAddresses(addresses)
	c.IMs = unmarshalIMs(ims)
	c.URLs = unmarshalURLs(urls)
	c.IsFavorite = favorite == 1
	if birthday.Valid {
		t := birthday.Time.UTC()
		c.Birthday = &t
	}
	return &c, nil
}

func scanGroup(row scanner) (*Group, error) {
	var g Group
	err := row.Scan(&g.ID, &g.UserID, &g.Name, &g.Color, &g.CreatedAt, &g.UpdatedAt, &g.Count)
	if err != nil {
		return nil, err
	}
	return &g, nil
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

// likeEscape escapes LIKE wildcards (\, %, _) so a search term is matched
// literally. Pair with "LIKE ? ESCAPE '\\'".
func likeEscape(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch r {
		case '\\', '%', '_':
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}

func sortClause(by string, desc bool) string {
	col := "display_name COLLATE NOCASE"
	switch by {
	case "created":
		col = "created_at"
	case "updated":
		col = "updated_at"
	case "company":
		col = "company COLLATE NOCASE"
	}
	dir := "ASC"
	if desc {
		dir = "DESC"
	}
	return fmt.Sprintf("%s %s, given_name COLLATE NOCASE ASC", col, dir)
}

// isUniqueViolation reports whether err is a SQLite UNIQUE/PK constraint
// failure (modernc.org/sqlite prefixes constraint errors with "constraint").
func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "constraint failed") ||
		strings.Contains(msg, "UNIQUE constraint failed")
}

// --- JSON helpers ----------------------------------------------------------

func marshalJSON[T any](v []T) string {
	if len(v) == 0 {
		return "[]"
	}
	b, _ := json.Marshal(v)
	return string(b)
}

func unmarshalJSON[T any](s string) []T {
	if s == "" {
		return nil
	}
	var out []T
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		return nil
	}
	return out
}

func unmarshalEmails(s string) []Email      { return unmarshalJSON[Email](s) }
func unmarshalPhones(s string) []Phone      { return unmarshalJSON[Phone](s) }
func unmarshalAddresses(s string) []Address { return unmarshalJSON[Address](s) }
func unmarshalIMs(s string) []IM            { return unmarshalJSON[IM](s) }
func unmarshalURLs(s string) []URL          { return unmarshalJSON[URL](s) }
