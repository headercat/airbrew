package user

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

// Repository persists users and their password credentials.
type Repository struct {
	db *sql.DB
}

// NewRepository returns a Repository bound to db.
func NewRepository(db *sql.DB) *Repository { return &Repository{db: db} }

// ErrNotFound is returned when no row matches the lookup.
var ErrNotFound = errors.New("user not found")

// ErrEmailTaken is returned by Create when the email is already registered.
var ErrEmailTaken = errors.New("email already taken")

var errPasswordHistoryUnavailable = errors.New("password history unavailable")

// IsPasswordHistoryUnavailable reports whether err means the password_history
// table is absent in a test or legacy database.
func IsPasswordHistoryUnavailable(err error) bool {
	return errors.Is(err, errPasswordHistoryUnavailable)
}

const (
	columnsRead = `id, public_subject, email, email_verified, status, role,
		COALESCE(display_name, ''), COALESCE(description, ''),
		birthday, COALESCE(phone_number, ''), COALESCE(avatar_url, ''),
		COALESCE(custom_fields, '{}'), created_at, updated_at`
)

// Create inserts a user and their password credentials in a single transaction.
func (r *Repository) Create(ctx context.Context, u *User, passwordHash string) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("user: begin tx: %w", err)
	}
	defer tx.Rollback()

	var existing string
	err = tx.QueryRowContext(ctx, "SELECT id FROM users WHERE email = ?", u.Email).Scan(&existing)
	if err == nil {
		return ErrEmailTaken
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("user: check email: %w", err)
	}

	now := time.Now().UTC().Truncate(time.Second)
	u.CreatedAt = now
	u.UpdatedAt = now
	if u.Role == "" {
		u.Role = RoleUser
	}
	customFields, err := marshalCustomFields(u.CustomFields)
	if err != nil {
		return fmt.Errorf("user: marshal custom fields: %w", err)
	}

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO users
		  (id, public_subject, email, status, role, email_verified,
		   display_name, description, birthday, phone_number, avatar_url,
		   custom_fields, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		u.ID, u.PublicSubject, u.Email, string(u.Status), string(u.Role), u.EmailVerified,
		nullable(u.DisplayName), nullable(u.Description),
		nullableTime(u.Birthday), nullable(u.PhoneNumber), nullable(u.AvatarURL),
		customFields, now, now,
	); err != nil {
		return fmt.Errorf("user: insert: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO password_credentials (user_id, password_hash, password_alg, created_at, updated_at)
		VALUES (?, ?, 'argon2id', ?, ?)
	`, u.ID, passwordHash, now, now); err != nil {
		return fmt.Errorf("user: insert credentials: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("user: commit: %w", err)
	}
	return nil
}

// GetByID returns the user with the given internal ID.
func (r *Repository) GetByID(ctx context.Context, id string) (*User, error) {
	return r.queryOne(ctx, `SELECT `+columnsRead+` FROM users WHERE id = ?`, id)
}

// GetByEmail returns the user with the given email (case-insensitive).
func (r *Repository) GetByEmail(ctx context.Context, email string) (*User, error) {
	return r.queryOne(ctx, `SELECT `+columnsRead+` FROM users WHERE email = ?`, email)
}

// CountByRole returns active-or-suspended users with the given role. Deleted
// users are ignored so deleting the only admin intentionally re-enables
// bootstrap recovery on next startup.
func (r *Repository) CountByRole(ctx context.Context, role Role) (int, error) {
	var n int
	if err := r.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM users WHERE role = ? AND status != ?",
		string(role), string(StatusDeleted),
	).Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

// List returns up to limit users (excluding soft-deleted), ordered by creation
// time descending. Pass limit=0 for the default page size.
func (r *Repository) List(ctx context.Context, limit, offset int) ([]*User, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	rows, err := r.db.QueryContext(ctx,
		"SELECT "+columnsRead+" FROM users WHERE status != ? ORDER BY created_at DESC LIMIT ? OFFSET ?",
		string(StatusDeleted), limit, offset,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*User
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// Search returns users matching the query string against email or display_name
// (case-insensitive), excluding soft-deleted users.
func (r *Repository) Search(ctx context.Context, query string, limit, offset int) ([]*User, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	if query == "" {
		return r.List(ctx, limit, offset)
	}
	pattern := "%" + query + "%"
	rows, err := r.db.QueryContext(ctx,
		"SELECT "+columnsRead+` FROM users
		WHERE status != ? AND (email LIKE ? ESCAPE '\' OR COALESCE(display_name,'') LIKE ? ESCAPE '\')
		ORDER BY created_at DESC LIMIT ? OFFSET ?`,
		string(StatusDeleted), pattern, pattern, limit, offset,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*User
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// SetRole updates only the role column.
func (r *Repository) SetRole(ctx context.Context, userID string, role Role) error {
	now := time.Now().UTC().Truncate(time.Second)
	res, err := r.db.ExecContext(ctx,
		"UPDATE users SET role = ?, updated_at = ? WHERE id = ?",
		string(role), now, userID,
	)
	if err != nil {
		return fmt.Errorf("user: set role: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// SetStatus updates only the status column.
func (r *Repository) SetStatus(ctx context.Context, userID string, status Status) error {
	now := time.Now().UTC().Truncate(time.Second)
	var deletedAt any
	if status == StatusDeleted {
		deletedAt = now
	}
	res, err := r.db.ExecContext(ctx,
		"UPDATE users SET status = ?, deleted_at = ?, updated_at = ? WHERE id = ?",
		string(status), deletedAt, now, userID,
	)
	if err != nil {
		return fmt.Errorf("user: set status: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// Count returns the total number of non-deleted users.
func (r *Repository) Count(ctx context.Context) (int, error) {
	var n int
	err := r.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM users WHERE status != ?",
		string(StatusDeleted),
	).Scan(&n)
	return n, err
}

// GetPasswordHash returns the stored argon2id hash for the user.
func (r *Repository) GetPasswordHash(ctx context.Context, userID string) (string, error) {
	var hash string
	err := r.db.QueryRowContext(ctx,
		"SELECT password_hash FROM password_credentials WHERE user_id = ?", userID,
	).Scan(&hash)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	return hash, nil
}

// UpdatePassword replaces the stored argon2id hash for the user and records the
// change timestamp in password_changed_at (added by migration 0004) so the
// password policy max-age check can be evaluated.
func (r *Repository) UpdatePassword(ctx context.Context, userID, passwordHash string) error {
	if err := r.rememberCurrentPassword(ctx, userID); err != nil && !errors.Is(err, errPasswordHistoryUnavailable) {
		return err
	}
	now := time.Now().UTC().Truncate(time.Second)
	res, err := r.db.ExecContext(ctx, `
		UPDATE password_credentials
		   SET password_hash = ?, password_changed_at = ?, updated_at = ?
		 WHERE user_id = ?
	`, passwordHash, now, now, userID)
	if err != nil {
		return fmt.Errorf("user: update password: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *Repository) rememberCurrentPassword(ctx context.Context, userID string) error {
	current, err := r.GetPasswordHash(ctx, userID)
	if err != nil {
		return err
	}
	_, err = r.db.ExecContext(ctx, `
		INSERT INTO password_history (id, user_id, password_hash, created_at)
		VALUES (?, ?, ?, ?)
	`, id.New(), userID, current, time.Now().UTC().Truncate(time.Second))
	if isNoSuchTable(err) {
		return errPasswordHistoryUnavailable
	}
	return err
}

// RecentPasswordHashes returns the most recent previous hashes for userID.
func (r *Repository) RecentPasswordHashes(ctx context.Context, userID string, limit int) ([]string, error) {
	if limit <= 0 {
		return nil, nil
	}
	rows, err := r.db.QueryContext(ctx, `
		SELECT password_hash
		FROM password_history
		WHERE user_id = ?
		ORDER BY created_at DESC
		LIMIT ?
	`, userID, limit)
	if isNoSuchTable(err) {
		return nil, errPasswordHistoryUnavailable
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var hash string
		if err := rows.Scan(&hash); err != nil {
			return nil, err
		}
		out = append(out, hash)
	}
	return out, rows.Err()
}

// PrunePasswordHistory keeps only the newest keep rows for userID.
func (r *Repository) PrunePasswordHistory(ctx context.Context, userID string, keep int) error {
	if keep < 0 {
		keep = 0
	}
	_, err := r.db.ExecContext(ctx, `
		DELETE FROM password_history
		WHERE user_id = ?
		  AND id NOT IN (
			SELECT id FROM password_history
			WHERE user_id = ?
			ORDER BY created_at DESC
			LIMIT ?
		  )
	`, userID, userID, keep)
	if isNoSuchTable(err) {
		return errPasswordHistoryUnavailable
	}
	return err
}

func isNoSuchTable(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "no such table")
}

// PasswordChangedAt returns the timestamp of the last password change, or the
// credentials' updated_at when the column is unset (e.g. rows created before
// migration 0004).
func (r *Repository) PasswordChangedAt(ctx context.Context, userID string) (time.Time, error) {
	var changed, updated sql.NullTime
	err := r.db.QueryRowContext(ctx,
		"SELECT password_changed_at, updated_at FROM password_credentials WHERE user_id = ?",
		userID,
	).Scan(&changed, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, ErrNotFound
	}
	if err != nil {
		return time.Time{}, err
	}
	if changed.Valid {
		return changed.Time, nil
	}
	if updated.Valid {
		return updated.Time, nil
	}
	return time.Time{}, nil
}

// ProfilePatch describes partial updates to a user's profile. nil fields are
// left untouched; non-nil fields (including empty strings) overwrite.
type ProfilePatch struct {
	DisplayName  *string
	Description  *string
	Birthday     *time.Time
	PhoneNumber  *string
	AvatarURL    *string
	CustomFields *map[string]string
}

// UpdateProfile applies a partial update to a user's profile fields.
func (r *Repository) UpdateProfile(ctx context.Context, userID string, patch ProfilePatch) error {
	now := time.Now().UTC().Truncate(time.Second)
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("user: begin tx: %w", err)
	}
	defer tx.Rollback()

	if patch.CustomFields != nil {
		raw, err := marshalCustomFields(*patch.CustomFields)
		if err != nil {
			return fmt.Errorf("user: marshal custom fields: %w", err)
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE users SET custom_fields = ?, updated_at = ? WHERE id = ?`,
			raw, now, userID,
		); err != nil {
			return fmt.Errorf("user: update custom_fields: %w", err)
		}
	}
	if patch.DisplayName != nil {
		if _, err := tx.ExecContext(ctx,
			`UPDATE users SET display_name = ?, updated_at = ? WHERE id = ?`,
			nullable(*patch.DisplayName), now, userID,
		); err != nil {
			return fmt.Errorf("user: update display_name: %w", err)
		}
	}
	if patch.Description != nil {
		if _, err := tx.ExecContext(ctx,
			`UPDATE users SET description = ?, updated_at = ? WHERE id = ?`,
			nullable(*patch.Description), now, userID,
		); err != nil {
			return fmt.Errorf("user: update description: %w", err)
		}
	}
	if patch.Birthday != nil {
		var value any
		if !patch.Birthday.IsZero() {
			value = *patch.Birthday
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE users SET birthday = ?, updated_at = ? WHERE id = ?`,
			value, now, userID,
		); err != nil {
			return fmt.Errorf("user: update birthday: %w", err)
		}
	}
	if patch.PhoneNumber != nil {
		if _, err := tx.ExecContext(ctx,
			`UPDATE users SET phone_number = ?, updated_at = ? WHERE id = ?`,
			nullable(*patch.PhoneNumber), now, userID,
		); err != nil {
			return fmt.Errorf("user: update phone_number: %w", err)
		}
	}
	if patch.AvatarURL != nil {
		if _, err := tx.ExecContext(ctx,
			`UPDATE users SET avatar_url = ?, updated_at = ? WHERE id = ?`,
			nullable(*patch.AvatarURL), now, userID,
		); err != nil {
			return fmt.Errorf("user: update avatar_url: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("user: commit: %w", err)
	}
	return nil
}

func (r *Repository) queryOne(ctx context.Context, query string, args ...any) (*User, error) {
	row := r.db.QueryRowContext(ctx, query, args...)
	return scanUser(row)
}

// scanner abstracts *sql.Row and *sql.Rows so queryOne and List can share
// the column-mapping logic.
type scanner interface {
	Scan(dest ...any) error
}

func scanUser(row scanner) (*User, error) {
	u := &User{}
	var status, role string
	var verified int
	var customFields string
	var displayName, description, phoneNumber, avatarURL sql.NullString
	var birthday sql.NullTime
	err := row.Scan(
		&u.ID, &u.PublicSubject, &u.Email, &verified, &status, &role,
		&displayName, &description, &birthday, &phoneNumber, &avatarURL,
		&customFields, &u.CreatedAt, &u.UpdatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	u.Status = Status(status)
	u.Role = Role(role)
	u.EmailVerified = verified == 1
	if displayName.Valid {
		u.DisplayName = displayName.String
	}
	if description.Valid {
		u.Description = description.String
	}
	if birthday.Valid {
		t := birthday.Time.UTC()
		u.Birthday = &t
	}
	if phoneNumber.Valid {
		u.PhoneNumber = phoneNumber.String
	}
	if avatarURL.Valid {
		u.AvatarURL = avatarURL.String
	}
	u.CustomFields = unmarshalCustomFields(customFields)
	return u, nil
}

func marshalCustomFields(m map[string]string) (string, error) {
	if m == nil {
		return "{}", nil
	}
	b, err := json.Marshal(m)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func unmarshalCustomFields(s string) map[string]string {
	if s == "" {
		return map[string]string{}
	}
	var m map[string]string
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		return map[string]string{}
	}
	if m == nil {
		return map[string]string{}
	}
	return m
}

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
