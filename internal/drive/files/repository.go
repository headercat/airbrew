package files

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/headercat/airbrew/internal/id"
)

// Repository persists drive nodes and shares.
type Repository struct {
	db *sql.DB
}

// NewRepository returns a Repository bound to db.
func NewRepository(db *sql.DB) *Repository { return &Repository{db: db} }

// configSettingKey is the server_settings key holding the drive limits JSON.
const configSettingKey = "module.drive.config"

// GetConfig reads the stored drive config JSON, applying defaults. A missing or
// unreadable row falls back to defaults so startup never blocks.
func (r *Repository) GetConfig(ctx context.Context) Config {
	var raw string
	err := r.db.QueryRowContext(ctx,
		"SELECT value FROM server_settings WHERE key = ?", configSettingKey).Scan(&raw)
	if err != nil {
		return ParseConfig("")
	}
	return ParseConfig(raw)
}

// SetConfig persists the drive config JSON (upsert).
func (r *Repository) SetConfig(ctx context.Context, c Config) error {
	now := time.Now().UTC().Truncate(time.Second)
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO server_settings (key, value, updated_at) VALUES (?, ?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at
	`, configSettingKey, MarshalConfig(c), now)
	return err
}

const nodeColumns = `id, user_id, COALESCE(parent_id,''), kind, name,
	COALESCE(blob_path,''), COALESCE(content_type,''), size_bytes, COALESCE(sha256,''),
	is_starred, deleted_at, created_at, updated_at`

// CreateNode inserts a node row.
func (r *Repository) CreateNode(ctx context.Context, n *Node) error {
	now := time.Now().UTC().Truncate(time.Second)
	n.CreatedAt = now
	n.UpdatedAt = now
	if _, err := r.db.ExecContext(ctx, `
		INSERT INTO drive_nodes
		  (id, user_id, parent_id, kind, name, blob_path, content_type,
		   size_bytes, sha256, is_starred, deleted_at, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		n.ID, n.UserID, nullableParent(n.ParentID), string(n.Kind), n.Name,
		nullable(n.BlobPath), nullable(n.ContentType), n.SizeBytes, nullable(n.SHA256),
		boolToInt(n.IsStarred), nil, now, now,
	); err != nil {
		return fmt.Errorf("drive: insert node: %w", err)
	}
	return nil
}

// CreateNodeWithQuota inserts a file node and enforces the per-user quota
// atomically in one transaction, closing the TOCTOU window between a standalone
// size read and the insert. quota <= 0 disables the check (unlimited).
func (r *Repository) CreateNodeWithQuota(ctx context.Context, n *Node, quota int64) error {
	now := time.Now().UTC().Truncate(time.Second)
	n.CreatedAt = now
	n.UpdatedAt = now
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("drive: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }() //nolint:errcheck
	if quota > 0 {
		var used sql.NullInt64
		if err := tx.QueryRowContext(ctx,
			"SELECT COALESCE(SUM(size_bytes),0) FROM drive_nodes WHERE user_id = ? AND kind = 'file' AND deleted_at IS NULL",
			n.UserID).Scan(&used); err != nil {
			return fmt.Errorf("drive: quota read: %w", err)
		}
		if used.Int64+n.SizeBytes > quota {
			return ErrQuotaExceeded
		}
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO drive_nodes
		  (id, user_id, parent_id, kind, name, blob_path, content_type,
		   size_bytes, sha256, is_starred, deleted_at, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		n.ID, n.UserID, nullableParent(n.ParentID), string(n.Kind), n.Name,
		nullable(n.BlobPath), nullable(n.ContentType), n.SizeBytes, nullable(n.SHA256),
		boolToInt(n.IsStarred), nil, now, now,
	); err != nil {
		return fmt.Errorf("drive: insert node: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("drive: commit node: %w", err)
	}
	return nil
}

// GetNode returns one live node owned by userID.
func (r *Repository) GetNode(ctx context.Context, userID, id string) (*Node, error) {
	row := r.db.QueryRowContext(ctx,
		"SELECT "+nodeColumns+" FROM drive_nodes WHERE id = ? AND user_id = ? AND deleted_at IS NULL", id, userID)
	n, err := scanNode(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return n, err
}

// GetNodeAny returns one node owned by userID regardless of trash state.
func (r *Repository) GetNodeAny(ctx context.Context, userID, id string) (*Node, error) {
	row := r.db.QueryRowContext(ctx,
		"SELECT "+nodeColumns+" FROM drive_nodes WHERE id = ? AND user_id = ?", id, userID)
	n, err := scanNode(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return n, err
}

// ListFilter controls which nodes are returned (declared in files.go).

// applyListFilter appends the filter's WHERE clauses (excluding ORDER/LIMIT) to
// q and returns the accumulated args. Shared by ListNodes and CountNodes.
func applyListFilter(q string, f ListFilter, args []any) (string, []any) {
	switch f.Folder {
	case "trash":
		q += " AND deleted_at IS NOT NULL"
		// Show only top-level trashed items (whose parent is not itself trashed)
		// so a trashed folder's children aren't listed separately.
		q += " AND NOT EXISTS (SELECT 1 FROM drive_nodes p WHERE p.id = drive_nodes.parent_id AND p.deleted_at IS NOT NULL)"
	case "starred":
		q += " AND deleted_at IS NULL AND is_starred = 1"
	case "search":
		q += " AND deleted_at IS NULL"
		if s := strings.TrimSpace(f.Search); s != "" {
			q += " AND name LIKE ? ESCAPE '\\'"
			args = append(args, "%"+likeEscape(s)+"%")
		}
	default: // regular listing
		q += " AND deleted_at IS NULL"
		if f.ParentID == "*" {
			// any parent (used by "all files" overviews)
		} else if f.ParentID == "" {
			q += " AND parent_id IS NULL"
		} else {
			q += " AND parent_id = ?"
			args = append(args, f.ParentID)
		}
	}
	if f.Kind != "" {
		q += " AND kind = ?"
		args = append(args, string(f.Kind))
	}
	return q, args
}

// ListNodes returns nodes matching the filter, sorted.
func (r *Repository) ListNodes(ctx context.Context, f ListFilter) ([]*Node, error) {
	if f.Limit <= 0 || f.Limit > 200 {
		f.Limit = 100
	}
	if f.Offset < 0 {
		f.Offset = 0
	}
	q, args := applyListFilter(
		"SELECT "+nodeColumns+" FROM drive_nodes WHERE user_id = ?", f, []any{f.UserID})
	q += " ORDER BY " + sortClause(f.SortBy, f.SortDesc)
	q += " LIMIT ? OFFSET ?"
	args = append(args, f.Limit, f.Offset)

	rows, err := r.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Node
	for rows.Next() {
		n, err := scanNode(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// CountNodesFiltered returns the total number of nodes matching the filter
// (ignoring limit/offset), for pagination.
func (r *Repository) CountNodesFiltered(ctx context.Context, f ListFilter) (int, error) {
	q, args := applyListFilter(
		"SELECT COUNT(*) FROM drive_nodes WHERE user_id = ?", f, []any{f.UserID})
	var n int
	if err := r.db.QueryRowContext(ctx, q, args...).Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

// CountNodes returns the number of live nodes matching a parent.
func (r *Repository) CountNodes(ctx context.Context, userID, parentID string) (int, error) {
	q := "SELECT COUNT(*) FROM drive_nodes WHERE user_id = ? AND deleted_at IS NULL"
	args := []any{userID}
	if parentID == "" {
		q += " AND parent_id IS NULL"
	} else {
		q += " AND parent_id = ?"
		args = append(args, parentID)
	}
	var n int
	if err := r.db.QueryRowContext(ctx, q, args...).Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

// ListChildren returns the live direct children of a folder in stable
// folder-first/name order. It is used by recursive folder copy.
func (r *Repository) ListChildren(ctx context.Context, userID, parentID string) ([]*Node, error) {
	q := "SELECT " + nodeColumns + " FROM drive_nodes WHERE user_id = ? AND deleted_at IS NULL"
	args := []any{userID}
	if parentID == "" {
		q += " AND parent_id IS NULL"
	} else {
		q += " AND parent_id = ?"
		args = append(args, parentID)
	}
	q += " ORDER BY CASE kind WHEN 'folder' THEN 0 ELSE 1 END, lower(name) ASC, name ASC, created_at DESC"
	rows, err := r.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Node
	for rows.Next() {
		n, err := scanNode(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// SubtreeSize returns the live file byte total under rootID, inclusive.
func (r *Repository) SubtreeSize(ctx context.Context, userID, rootID string) (int64, error) {
	var total sql.NullInt64
	err := r.db.QueryRowContext(ctx, `
		WITH RECURSIVE subtree(id) AS (
		  SELECT id FROM drive_nodes WHERE id = ? AND user_id = ? AND deleted_at IS NULL
		  UNION ALL
		  SELECT c.id FROM drive_nodes c JOIN subtree ON c.parent_id = subtree.id
		    WHERE c.user_id = ? AND c.deleted_at IS NULL
		)
		SELECT COALESCE(SUM(size_bytes),0)
		FROM drive_nodes
		WHERE id IN (SELECT id FROM subtree) AND kind = 'file'`,
		rootID, userID, userID).Scan(&total)
	if err != nil {
		return 0, err
	}
	return total.Int64, nil
}

// UpdateNodeFields applies a partial update to name/parent/content metadata.
type UpdateNodeFields struct {
	Name        *string
	ParentID    *string // pointer so "" (root) is distinguishable from "unchanged"
	ContentType *string
	SizeBytes   *int64
	BlobPath    *string
	SHA256      *string
}

// UpdateNode applies a partial update.
func (r *Repository) UpdateNode(ctx context.Context, userID, id string, upd UpdateNodeFields) error {
	sets := []string{}
	args := []any{}
	if upd.Name != nil {
		sets = append(sets, "name = ?")
		args = append(args, *upd.Name)
	}
	if upd.ParentID != nil {
		sets = append(sets, "parent_id = ?")
		args = append(args, nullableParent(*upd.ParentID))
	}
	if upd.ContentType != nil {
		sets = append(sets, "content_type = ?")
		args = append(args, nullable(*upd.ContentType))
	}
	if upd.SizeBytes != nil {
		sets = append(sets, "size_bytes = ?")
		args = append(args, *upd.SizeBytes)
	}
	if upd.BlobPath != nil {
		sets = append(sets, "blob_path = ?")
		args = append(args, nullable(*upd.BlobPath))
	}
	if upd.SHA256 != nil {
		sets = append(sets, "sha256 = ?")
		args = append(args, nullable(*upd.SHA256))
	}
	if len(sets) == 0 {
		return nil
	}
	sets = append(sets, "updated_at = ?")
	args = append(args, time.Now().UTC().Truncate(time.Second), id, userID)
	res, err := r.db.ExecContext(ctx,
		"UPDATE drive_nodes SET "+strings.Join(sets, ", ")+" WHERE id = ? AND user_id = ? AND deleted_at IS NULL", args...)
	if err != nil {
		return fmt.Errorf("drive: update node: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// PatchStar sets the starred flag.
func (r *Repository) PatchStar(ctx context.Context, userID, id string, starred bool) error {
	res, err := r.db.ExecContext(ctx,
		"UPDATE drive_nodes SET is_starred = ?, updated_at = ? WHERE id = ? AND user_id = ? AND deleted_at IS NULL",
		boolToInt(starred), time.Now().UTC().Truncate(time.Second), id, userID)
	if err != nil {
		return fmt.Errorf("drive: star node: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// Trash soft-deletes a node and (for folders) all of its descendants.
func (r *Repository) Trash(ctx context.Context, userID, id string) error {
	now := time.Now().UTC().Truncate(time.Second)
	// Trash the node plus its whole subtree via a recursive CTE.
	res, err := r.db.ExecContext(ctx, `
		WITH RECURSIVE subtree(id) AS (
		  SELECT id FROM drive_nodes WHERE id = ? AND user_id = ? AND deleted_at IS NULL
		  UNION ALL
		  SELECT c.id FROM drive_nodes c JOIN subtree ON c.parent_id = subtree.id
		    WHERE c.deleted_at IS NULL AND c.user_id = ?
		)
		UPDATE drive_nodes SET deleted_at = ?, updated_at = ?
		WHERE id IN (SELECT id FROM subtree)
	`, id, userID, userID, now, now)
	if err != nil {
		return fmt.Errorf("drive: trash: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// Restore clears the soft-delete flag for a node and the descendants that were
// trashed together with it (same cascade timestamp). A descendant that was
// trashed independently — before or after the folder — stays in the trash.
func (r *Repository) Restore(ctx context.Context, userID, id string) error {
	n, err := r.GetNodeAny(ctx, userID, id)
	if err != nil {
		return err
	}
	if n.DeletedAt == nil {
		return ErrNotFound
	}
	marker := *n.DeletedAt
	now := time.Now().UTC().Truncate(time.Second)
	res, err := r.db.ExecContext(ctx, `
		WITH RECURSIVE subtree(id) AS (
		  SELECT id FROM drive_nodes WHERE id = ? AND user_id = ?
		  UNION ALL
		  SELECT c.id FROM drive_nodes c JOIN subtree ON c.parent_id = subtree.id
		    WHERE c.user_id = ?
		)
		UPDATE drive_nodes SET deleted_at = NULL, updated_at = ?
		WHERE id IN (SELECT id FROM subtree) AND deleted_at = ?
	`, id, userID, userID, now, marker)
	if err != nil {
		return fmt.Errorf("drive: restore: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// DeletePermanent hard-deletes a node and its subtree. It returns the blob
// paths that were dropped so the caller can purge them after commit succeeds.
// The collect + delete run in one transaction so the blob list exactly matches
// the deleted rows (no concurrent Restore can interleave).
func (r *Repository) DeletePermanent(ctx context.Context, userID, id string) ([]string, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("drive: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }() //nolint:errcheck

	rows, err := tx.QueryContext(ctx, `
		WITH RECURSIVE subtree(id) AS (
		  SELECT id FROM drive_nodes WHERE id = ? AND user_id = ?
		  UNION ALL
		  SELECT c.id FROM drive_nodes c JOIN subtree ON c.parent_id = subtree.id
		    WHERE c.user_id = ?
		)
		SELECT COALESCE(blob_path,'') FROM drive_nodes WHERE id IN (SELECT id FROM subtree) AND blob_path IS NOT NULL`,
		id, userID, userID)
	if err != nil {
		return nil, fmt.Errorf("drive: collect blobs: %w", err)
	}
	var paths []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			rows.Close()
			return nil, err
		}
		if p != "" {
			paths = append(paths, p)
		}
	}
	rows.Close()

	res, err := tx.ExecContext(ctx, `
		WITH RECURSIVE subtree(id) AS (
		  SELECT id FROM drive_nodes WHERE id = ? AND user_id = ?
		  UNION ALL
		  SELECT c.id FROM drive_nodes c JOIN subtree ON c.parent_id = subtree.id
		    WHERE c.user_id = ?
		)
		DELETE FROM drive_nodes WHERE id IN (SELECT id FROM subtree)`,
		id, userID, userID)
	if err != nil {
		return nil, fmt.Errorf("drive: permanent delete: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, ErrNotFound
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("drive: commit permanent delete: %w", err)
	}
	return paths, nil
}

// EmptyTrash hard-deletes every trashed node for a user, returning dropped blob
// paths. The collect + delete run in one transaction so a concurrent Restore
// cannot resurrect a row whose blob is about to be purged.
func (r *Repository) EmptyTrash(ctx context.Context, userID string) ([]string, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("drive: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }() //nolint:errcheck

	rows, err := tx.QueryContext(ctx,
		"SELECT COALESCE(blob_path,'') FROM drive_nodes WHERE user_id = ? AND deleted_at IS NOT NULL AND blob_path IS NOT NULL",
		userID)
	if err != nil {
		return nil, err
	}
	var paths []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			rows.Close()
			return nil, err
		}
		if p != "" {
			paths = append(paths, p)
		}
	}
	rows.Close()

	if _, err := tx.ExecContext(ctx,
		"DELETE FROM drive_nodes WHERE user_id = ? AND deleted_at IS NOT NULL", userID); err != nil {
		return nil, fmt.Errorf("drive: empty trash: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("drive: commit empty trash: %w", err)
	}
	return paths, nil
}

// IsDescendant reports whether maybeDescID is in the subtree rooted at rootID
// (inclusive of rootID). Used for circular-move detection.
func (r *Repository) IsDescendant(ctx context.Context, userID, rootID, maybeDescID string) (bool, error) {
	if rootID == maybeDescID {
		return true, nil
	}
	var cur = maybeDescID
	for cur != "" {
		var parent sql.NullString
		err := r.db.QueryRowContext(ctx,
			"SELECT parent_id FROM drive_nodes WHERE id = ? AND user_id = ?", cur, userID).Scan(&parent)
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		if !parent.Valid {
			return false, nil
		}
		if parent.String == rootID {
			return true, nil
		}
		cur = parent.String
	}
	return false, nil
}

// GetPath returns the chain of ancestors from root down to (and including) the
// node, in one recursive query. Used to render breadcrumbs without N round
// trips. A trashed node's chain is still resolved so the trash UI can show it.
func (r *Repository) GetPath(ctx context.Context, userID, id string) ([]*Node, error) {
	rows, err := r.db.QueryContext(ctx, `
		WITH RECURSIVE chain(id) AS (
		  SELECT id FROM drive_nodes WHERE id = ? AND user_id = ?
		  UNION ALL
		  SELECT n.id FROM drive_nodes n JOIN chain ON n.id = (SELECT parent_id FROM drive_nodes WHERE id = chain.id)
		)
		SELECT `+nodeColumns+` FROM drive_nodes WHERE id IN (SELECT id FROM chain)`,
		id, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	byID := map[string]*Node{}
	var order []string
	for rows.Next() {
		n, err := scanNode(rows)
		if err != nil {
			return nil, err
		}
		byID[n.ID] = n
		order = append(order, n.ID)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(byID) == 0 {
		return nil, ErrNotFound
	}
	// Build root→node ordering by walking parent_id up from the target.
	var chain []*Node
	cur := byID[id]
	for cur != nil {
		chain = append([]*Node{cur}, chain...)
		if cur.ParentID == "" {
			break
		}
		cur = byID[cur.ParentID]
	}
	return chain, nil
}

// TotalSize returns the summed size of all live files owned by userID.
func (r *Repository) TotalSize(ctx context.Context, userID string) (int64, error) {
	var total sql.NullInt64
	err := r.db.QueryRowContext(ctx,
		"SELECT COALESCE(SUM(size_bytes),0) FROM drive_nodes WHERE user_id = ? AND kind = 'file' AND deleted_at IS NULL",
		userID).Scan(&total)
	if err != nil {
		return 0, err
	}
	return total.Int64, nil
}

// AllBlobPaths returns every blob_path for a user (live + trashed), for the
// janitor's orphan sweep.
func (r *Repository) AllBlobPaths(ctx context.Context, userID string) ([]string, error) {
	return r.allBlobPathsWhere(ctx,
		"SELECT COALESCE(blob_path,'') FROM drive_nodes WHERE user_id = ? AND blob_path IS NOT NULL",
		userID)
}

// AllBlobPathsAll returns every blob_path across all users.
func (r *Repository) AllBlobPathsAll(ctx context.Context) ([]string, error) {
	return r.allBlobPathsWhere(ctx, "SELECT COALESCE(blob_path,'') FROM drive_nodes WHERE blob_path IS NOT NULL")
}

func (r *Repository) allBlobPathsWhere(ctx context.Context, q string, args ...any) ([]string, error) {
	rows, err := r.db.QueryContext(ctx, q, args...)
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

// --- shares ----------------------------------------------------------------

const shareColumns = `id, node_id, user_id, token, CASE WHEN password_hash IS NOT NULL THEN 1 ELSE 0 END,
	expires_at, downloads, is_active, created_at, updated_at`

// shareColumnsForJoin qualifies the share columns with "s." and appends the
// joined node's name + a trashed flag (CASE). Used by ListSharesWithNode.
const shareColumnsForJoin = `s.id, s.node_id, s.user_id, s.token,
	CASE WHEN s.password_hash IS NOT NULL THEN 1 ELSE 0 END,
	s.expires_at, s.downloads, s.is_active, s.created_at, s.updated_at,
	COALESCE(n.name,''), CASE WHEN n.deleted_at IS NULL THEN 0 ELSE 1 END`

// CreateShare inserts a share row.
func (r *Repository) CreateShare(ctx context.Context, s *Share, passwordHash string) error {
	now := time.Now().UTC().Truncate(time.Second)
	s.CreatedAt = now
	s.UpdatedAt = now
	if _, err := r.db.ExecContext(ctx, `
		INSERT INTO drive_shares
		  (id, node_id, user_id, token, password_hash, expires_at, downloads, is_active, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		s.ID, s.NodeID, s.UserID, s.Token, nullable(passwordHash), nullableTime(s.ExpiresAt),
		s.Downloads, boolToInt(s.IsActive), now, now,
	); err != nil {
		if isUniqueViolation(err) {
			return ErrShareTokenTaken
		}
		return fmt.Errorf("drive: insert share: %w", err)
	}
	return nil
}

// GetShare returns one share owned by userID.
func (r *Repository) GetShare(ctx context.Context, userID, id string) (*Share, error) {
	row := r.db.QueryRowContext(ctx,
		"SELECT "+shareColumns+" FROM drive_shares WHERE id = ? AND user_id = ?", id, userID)
	s, err := scanShare(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrShareNotFound
	}
	return s, err
}

// ListShares returns the shares owned by userID.
func (r *Repository) ListShares(ctx context.Context, userID string) ([]*Share, error) {
	rows, err := r.db.QueryContext(ctx,
		"SELECT "+shareColumns+" FROM drive_shares WHERE user_id = ? ORDER BY created_at DESC", userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Share
	for rows.Next() {
		s, err := scanShare(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// ListSharesWithNode returns the owner's shares joined to their source node so
// the Shared view can label each link and flag a link whose source is trashed.
func (r *Repository) ListSharesWithNode(ctx context.Context, userID string) ([]*Share, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT `+shareColumnsForJoin+`
		FROM drive_shares s
		LEFT JOIN drive_nodes n ON n.id = s.node_id
		WHERE s.user_id = ?
		ORDER BY s.created_at DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Share
	for rows.Next() {
		s, err := scanShareWithNode(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// ListSharesByNode returns all shares for a node (active and revoked), so the
// owner's share dialog can list and revoke them.
func (r *Repository) ListSharesByNode(ctx context.Context, userID, nodeID string) ([]*Share, error) {
	rows, err := r.db.QueryContext(ctx,
		"SELECT "+shareColumns+" FROM drive_shares WHERE user_id = ? AND node_id = ? ORDER BY created_at DESC",
		userID, nodeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Share
	for rows.Next() {
		s, err := scanShare(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// GetShareByToken returns the share for a token plus its node (for public
// access). The node is returned only if it is live; the caller decides whether
// the share is still usable (active / not expired / password).
func (r *Repository) GetShareByToken(ctx context.Context, token string) (*Share, error) {
	row := r.db.QueryRowContext(ctx,
		"SELECT "+shareColumns+" FROM drive_shares WHERE token = ?", token)
	s, err := scanShare(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrShareNotFound
	}
	if err != nil {
		return nil, err
	}
	return s, nil
}

// GetShareNode returns the live node backing a share (file only).
func (r *Repository) GetShareNode(ctx context.Context, share *Share) (*Node, error) {
	row := r.db.QueryRowContext(ctx,
		"SELECT "+nodeColumns+" FROM drive_nodes WHERE id = ? AND kind = 'file' AND deleted_at IS NULL",
		share.NodeID)
	n, err := scanNode(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return n, err
}

// SharePasswordHash returns the stored argon2id hash for a password-protected
// share, used to verify a public access attempt.
func (r *Repository) SharePasswordHash(ctx context.Context, shareID string) (string, error) {
	var h sql.NullString
	err := r.db.QueryRowContext(ctx,
		"SELECT password_hash FROM drive_shares WHERE id = ?", shareID).Scan(&h)
	if err != nil {
		return "", err
	}
	if !h.Valid {
		return "", ErrPasswordRequired
	}
	return h.String, nil
}

// DeleteShare removes a share.
func (r *Repository) DeleteShare(ctx context.Context, userID, id string) error {
	res, err := r.db.ExecContext(ctx,
		"DELETE FROM drive_shares WHERE id = ? AND user_id = ?", id, userID)
	if err != nil {
		return fmt.Errorf("drive: delete share: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrShareNotFound
	}
	return nil
}

// IncrementShareDownloads atomically bumps the download counter.
func (r *Repository) IncrementShareDownloads(ctx context.Context, token string) error {
	_, err := r.db.ExecContext(ctx,
		"UPDATE drive_shares SET downloads = downloads + 1 WHERE token = ?", token)
	return err
}

// PurgeExpiredShares deactivates shares whose expiry has passed. Returns the
// count deactivated.
func (r *Repository) PurgeExpiredShares(ctx context.Context, now time.Time) (int, error) {
	res, err := r.db.ExecContext(ctx,
		"UPDATE drive_shares SET is_active = 0 WHERE expires_at IS NOT NULL AND expires_at < ? AND is_active = 1",
		now.UTC().Truncate(time.Second))
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// --- helpers ---------------------------------------------------------------

type scanner interface {
	Scan(dest ...any) error
}

func scanNode(row scanner) (*Node, error) {
	var n Node
	var kind string
	var starred int
	var deleted sql.NullTime
	err := row.Scan(&n.ID, &n.UserID, &n.ParentID, &kind, &n.Name,
		&n.BlobPath, &n.ContentType, &n.SizeBytes, &n.SHA256,
		&starred, &deleted, &n.CreatedAt, &n.UpdatedAt)
	if err != nil {
		return nil, err
	}
	n.Kind = Kind(kind)
	n.IsStarred = starred == 1
	if deleted.Valid {
		t := deleted.Time.UTC()
		n.DeletedAt = &t
	}
	return &n, nil
}

func scanShare(row scanner) (*Share, error) {
	var s Share
	var hasPW, active int
	var expires sql.NullTime
	err := row.Scan(&s.ID, &s.NodeID, &s.UserID, &s.Token, &hasPW, &expires, &s.Downloads, &active, &s.CreatedAt, &s.UpdatedAt)
	if err != nil {
		return nil, err
	}
	s.HasPassword = hasPW == 1
	s.IsActive = active == 1
	if expires.Valid {
		t := expires.Time.UTC()
		s.ExpiresAt = &t
	}
	return &s, nil
}

// scanShareWithNode scans a share row plus the joined node name + trashed flag
// (see shareColumnsForJoin).
func scanShareWithNode(row scanner) (*Share, error) {
	var s Share
	var hasPW, active, trashed int
	var expires sql.NullTime
	err := row.Scan(&s.ID, &s.NodeID, &s.UserID, &s.Token, &hasPW, &expires,
		&s.Downloads, &active, &s.CreatedAt, &s.UpdatedAt, &s.NodeName, &trashed)
	if err != nil {
		return nil, err
	}
	s.HasPassword = hasPW == 1
	s.IsActive = active == 1
	s.NodeTrashed = trashed == 1
	if expires.Valid {
		t := expires.Time.UTC()
		s.ExpiresAt = &t
	}
	return &s, nil
}

func nextID() string { return id.New() }

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// nullableParent maps "" (root) to SQL NULL for the parent_id column.
func nullableParent(parentID string) any {
	if parentID == "" {
		return nil
	}
	return parentID
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

// likeEscape escapes LIKE wildcards (\, %, _) so a user search term is matched
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
	col := "name"
	switch by {
	case "size":
		col = "size_bytes"
	case "created":
		col = "created_at"
	case "updated":
		col = "updated_at"
	}
	dir := "ASC"
	if desc {
		dir = "DESC"
	}
	// Folders first, then by the requested column, with name as a stable tiebreak.
	return fmt.Sprintf("kind ASC, %s %s, name ASC", col, dir)
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
