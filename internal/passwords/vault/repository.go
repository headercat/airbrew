package vault

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/headercat/airbrew/internal/id"
)

// Repository persists vault envelopes, folders, items and history.
type Repository struct {
	db *sql.DB
}

// NewRepository returns a Repository bound to db.
func NewRepository(db *sql.DB) *Repository { return &Repository{db: db} }

// SetupEnvelope stores the initial key envelope for a user. It fails with
// ErrEnvelopeExists if one is already present, so setup is idempotent-safe.
func (r *Repository) SetupEnvelope(ctx context.Context, env KeyEnvelope) error {
	now := time.Now().UTC().Truncate(time.Second)
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO vault_keys
		  (user_id, kdf_algorithm, kdf_salt, kdf_memory_kib, kdf_iterations,
		   kdf_parallelism, protected_vault_key, protected_vault_nonce,
		   crypto_version, version, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		env.UserID, env.KDFAlgorithm, env.KDFSalt,
		env.KDFMemoryKiB, env.KDFIterations, env.KDFParallelism,
		env.ProtectedVaultKey, env.ProtectedVaultNonce, env.CryptoVersion, 1, now, now,
	)
	if err != nil {
		// modernc.org/sqlite returns "constraint failed: UNIQUE" for PK dup.
		if isUniqueViolation(err) {
			return ErrEnvelopeExists
		}
		return fmt.Errorf("vault: insert envelope: %w", err)
	}
	return nil
}

// GetEnvelope returns the user's key envelope for client-side unlock.
func (r *Repository) GetEnvelope(ctx context.Context, userID string) (KeyEnvelope, error) {
	var env KeyEnvelope
	var alg string
	err := r.db.QueryRowContext(ctx, `
		SELECT user_id, kdf_algorithm, kdf_salt, kdf_memory_kib, kdf_iterations,
		       kdf_parallelism, protected_vault_key, protected_vault_nonce,
		       crypto_version, version, created_at, updated_at
		FROM vault_keys WHERE user_id = ?
	`, userID).Scan(
		&env.UserID, &alg, &env.KDFSalt, &env.KDFMemoryKiB, &env.KDFIterations,
		&env.KDFParallelism, &env.ProtectedVaultKey, &env.ProtectedVaultNonce,
		&env.CryptoVersion, &env.Version, &env.CreatedAt, &env.UpdatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return KeyEnvelope{}, ErrNotFound
	}
	if err != nil {
		return KeyEnvelope{}, err
	}
	env.KDFAlgorithm = alg
	return env, nil
}

// RotateEnvelope replaces the protected vault-key wrapper (and KDF params) for
// a master-password change. The underlying vault key stays the same, so no
// items are re-encrypted. ifVersion is the envelope version the caller last
// observed; on mismatch a *ConflictError is returned carrying the server's
// current envelope so the client can re-fetch and retry.
func (r *Repository) RotateEnvelope(ctx context.Context, env KeyEnvelope, ifVersion int64) error {
	now := time.Now().UTC().Truncate(time.Second)
	res, err := r.db.ExecContext(ctx, `
		UPDATE vault_keys SET
		  kdf_algorithm = ?, kdf_salt = ?, kdf_memory_kib = ?,
		  kdf_iterations = ?, kdf_parallelism = ?,
		  protected_vault_key = ?, protected_vault_nonce = ?,
		  crypto_version = ?, version = version + 1, updated_at = ?
		WHERE user_id = ? AND version = ?
	`,
		env.KDFAlgorithm, env.KDFSalt, env.KDFMemoryKiB,
		env.KDFIterations, env.KDFParallelism,
		env.ProtectedVaultKey, env.ProtectedVaultNonce, env.CryptoVersion, now, env.UserID, ifVersion,
	)
	if err != nil {
		return fmt.Errorf("vault: rotate envelope: %w", err)
	}
	switch n, _ := res.RowsAffected(); n {
	case 0:
		// Distinguish "no envelope" from "version mismatch" so the client gets
		// an actionable conflict rather than a generic not-found.
		cur, err := r.GetEnvelope(ctx, env.UserID)
		if errors.Is(err, ErrNotFound) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		return &ConflictError{CurrentRow: &cur}
	}
	return nil
}

// bumpRev increments and returns the next per-user sync revision inside tx.
// The same transaction that writes a row must bump the cursor, so a crash or
// rollback leaves both consistent.
func bumpRev(ctx context.Context, tx *sql.Tx, userID string) (int64, error) {
	var cur int64
	err := tx.QueryRowContext(ctx,
		"SELECT COALESCE((SELECT current_rev FROM vault_sync_state WHERE user_id = ?), 0)",
		userID,
	).Scan(&cur)
	if err != nil {
		return 0, fmt.Errorf("vault: read sync cursor: %w", err)
	}
	cur++
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO vault_sync_state (user_id, current_rev) VALUES (?, ?)
		ON CONFLICT(user_id) DO UPDATE SET current_rev = excluded.current_rev
	`, userID, cur); err != nil {
		return 0, fmt.Errorf("vault: bump sync cursor: %w", err)
	}
	return cur, nil
}

// currentRev returns the per-user sync cursor (0 if the user has written
// nothing yet).
func (r *Repository) currentRev(ctx context.Context, userID string) (int64, error) {
	var rev int64
	err := r.db.QueryRowContext(ctx,
		"SELECT COALESCE((SELECT current_rev FROM vault_sync_state WHERE user_id = ?), 0)",
		userID,
	).Scan(&rev)
	return rev, err
}

// CreateFolder inserts a folder and returns the stored row with its revision.
func (r *Repository) CreateFolder(ctx context.Context, userID, nameCipher, nameNonce string, cryptoVersion int) (Folder, error) {
	folder := Folder{ID: id.New(), UserID: userID, NameCipher: nameCipher, NameNonce: nameNonce, CryptoVersion: cryptoVersion}
	now := time.Now().UTC().Truncate(time.Second)
	err := inTx(ctx, r.db, func(tx *sql.Tx) error {
		rev, err := bumpRev(ctx, tx, userID)
		if err != nil {
			return err
		}
		folder.Revision = rev
		folder.CreatedAt = now
		folder.UpdatedAt = now
		_, err = tx.ExecContext(ctx, `
			INSERT INTO vault_folders
			  (id, user_id, name_cipher, name_nonce, crypto_version, revision, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		`, folder.ID, folder.UserID, folder.NameCipher, folder.NameNonce, folder.CryptoVersion, rev, now, now)
		if err != nil {
			return fmt.Errorf("vault: insert folder: %w", err)
		}
		return nil
	})
	if err != nil {
		return Folder{}, err
	}
	return folder, nil
}

// GetFolder returns one folder owned by userID.
func (r *Repository) GetFolder(ctx context.Context, userID, id string) (Folder, error) {
	row := r.db.QueryRowContext(ctx, folderSelect+` WHERE id = ? AND user_id = ?`, id, userID)
	f, err := scanFolder(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Folder{}, ErrNotFound
	}
	return f, err
}

// UpdateFolder renames a folder. If ifRevision does not match the stored
// revision, it returns a *ConflictError carrying the server's current row.
func (r *Repository) UpdateFolder(ctx context.Context, userID, id, nameCipher, nameNonce string, cryptoVersion int, ifRevision int64) (Folder, error) {
	var out Folder
	err := inTx(ctx, r.db, func(tx *sql.Tx) error {
		cur, err := lockFolderForWrite(ctx, tx, userID, id)
		if err != nil {
			return err
		}
		if cur.Revision != ifRevision {
			c := cur
			return &ConflictError{CurrentRow: &c}
		}
		rev, err := bumpRev(ctx, tx, userID)
		if err != nil {
			return err
		}
		now := time.Now().UTC().Truncate(time.Second)
		res, err := tx.ExecContext(ctx, `
			UPDATE vault_folders
			SET name_cipher = ?, name_nonce = ?, crypto_version = ?, revision = ?, updated_at = ?
			WHERE id = ? AND user_id = ? AND deleted_at IS NULL
		`, nameCipher, nameNonce, cryptoVersion, rev, now, id, userID)
		if err != nil {
			return fmt.Errorf("vault: update folder: %w", err)
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		out = cur
		out.NameCipher = nameCipher
		out.NameNonce = nameNonce
		out.CryptoVersion = cryptoVersion
		out.Revision = rev
		out.UpdatedAt = now
		return nil
	})
	if err != nil {
		return Folder{}, err
	}
	return out, nil
}

// SoftDeleteFolder tombstones a folder. Items referencing it keep their
// folder_id (ON DELETE SET NULL only fires on hard delete, which we never do).
func (r *Repository) SoftDeleteFolder(ctx context.Context, userID, id string, ifRevision int64) error {
	return inTx(ctx, r.db, func(tx *sql.Tx) error {
		cur, err := lockFolderForWrite(ctx, tx, userID, id)
		if err != nil {
			return err
		}
		if cur.Revision != ifRevision {
			c := cur
			return &ConflictError{CurrentRow: &c}
		}
		rev, err := bumpRev(ctx, tx, userID)
		if err != nil {
			return err
		}
		now := time.Now().UTC().Truncate(time.Second)
		res, err := tx.ExecContext(ctx, `
			UPDATE vault_folders
			SET deleted_at = ?, revision = ?, updated_at = ?
			WHERE id = ? AND user_id = ? AND deleted_at IS NULL
		`, now, rev, now, id, userID)
		if err != nil {
			return fmt.Errorf("vault: soft-delete folder: %w", err)
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE vault_items
			SET folder_id = NULL, revision = ?, updated_at = ?
			WHERE user_id = ? AND folder_id = ? AND deleted_at IS NULL
		`, rev, now, userID, id); err != nil {
			return fmt.Errorf("vault: clear deleted folder from items: %w", err)
		}
		return nil
	})
}

// CreateItem inserts an item, archives the previous revision when updating.
func (r *Repository) CreateItem(ctx context.Context, userID string, in ItemInput) (Item, error) {
	if !in.Type.Valid() {
		return Item{}, ErrInvalidInput
	}
	item := Item{
		ID: id.New(), UserID: userID, Type: in.Type, FolderID: in.FolderID,
		NameCipher: in.NameCipher, NameNonce: in.NameNonce,
		DataCipher: in.DataCipher, DataNonce: in.DataNonce,
		NotesCipher: in.NotesCipher, NotesNonce: in.NotesNonce,
		CryptoVersion: in.CryptoVersion,
		Favorite:      in.Favorite, Reprompt: in.Reprompt,
	}
	now := time.Now().UTC().Truncate(time.Second)
	err := inTx(ctx, r.db, func(tx *sql.Tx) error {
		if err := ensureFolderOwned(ctx, tx, userID, in.FolderID); err != nil {
			return err
		}
		rev, err := bumpRev(ctx, tx, userID)
		if err != nil {
			return err
		}
		item.Revision = rev
		item.CreatedAt = now
		item.UpdatedAt = now
		_, err = tx.ExecContext(ctx, `
			INSERT INTO vault_items
			  (id, user_id, type, folder_id, name_cipher, name_nonce,
			   data_cipher, data_nonce, notes_cipher, notes_nonce,
			   crypto_version, favorite, reprompt, revision, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		`,
			item.ID, item.UserID, string(item.Type), nullable(item.FolderID),
			item.NameCipher, item.NameNonce, item.DataCipher, item.DataNonce,
			nullable(item.NotesCipher), nullable(item.NotesNonce),
			item.CryptoVersion,
			boolToInt(item.Favorite), boolToInt(item.Reprompt), rev, now, now,
		)
		if err != nil {
			return fmt.Errorf("vault: insert item: %w", err)
		}
		return nil
	})
	if err != nil {
		return Item{}, err
	}
	return item, nil
}

// GetItem returns one non-deleted item owned by userID.
func (r *Repository) GetItem(ctx context.Context, userID, id string) (Item, error) {
	row := r.db.QueryRowContext(ctx,
		itemSelect+` WHERE id = ? AND user_id = ? AND deleted_at IS NULL`, id, userID)
	it, err := scanItem(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Item{}, ErrNotFound
	}
	return it, err
}

// UpdateItem overwrites an item's encrypted fields. The prior snapshot is
// archived into vault_item_revisions so clients can show history.
func (r *Repository) UpdateItem(ctx context.Context, userID, itemID string, in ItemInput, ifRevision int64) (Item, error) {
	if !in.Type.Valid() {
		return Item{}, ErrInvalidInput
	}
	var out Item
	err := inTx(ctx, r.db, func(tx *sql.Tx) error {
		cur, err := lockItemForWrite(ctx, tx, userID, itemID)
		if err != nil {
			return err
		}
		if cur.Revision != ifRevision {
			c := cur
			return &ConflictError{CurrentRow: &c}
		}
		if err := ensureFolderOwned(ctx, tx, userID, in.FolderID); err != nil {
			return err
		}
		if err := archiveItemSnapshot(ctx, tx, cur); err != nil {
			return fmt.Errorf("vault: archive item revision: %w", err)
		}
		rev, err := bumpRev(ctx, tx, userID)
		if err != nil {
			return err
		}
		now := time.Now().UTC().Truncate(time.Second)
		res, err := tx.ExecContext(ctx, `
			UPDATE vault_items SET
			  type = ?, folder_id = ?, name_cipher = ?, name_nonce = ?,
			  data_cipher = ?, data_nonce = ?, notes_cipher = ?, notes_nonce = ?,
			  crypto_version = ?, favorite = ?, reprompt = ?, revision = ?, updated_at = ?
			WHERE id = ? AND user_id = ? AND deleted_at IS NULL
		`,
			string(in.Type), nullable(in.FolderID), in.NameCipher, in.NameNonce,
			in.DataCipher, in.DataNonce, nullable(in.NotesCipher), nullable(in.NotesNonce),
			in.CryptoVersion, boolToInt(in.Favorite), boolToInt(in.Reprompt), rev, now, itemID, userID,
		)
		if err != nil {
			return fmt.Errorf("vault: update item: %w", err)
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		out = cur
		out.Type = in.Type
		out.FolderID = in.FolderID
		out.NameCipher = in.NameCipher
		out.NameNonce = in.NameNonce
		out.DataCipher = in.DataCipher
		out.DataNonce = in.DataNonce
		out.NotesCipher = in.NotesCipher
		out.NotesNonce = in.NotesNonce
		out.CryptoVersion = in.CryptoVersion
		out.Favorite = in.Favorite
		out.Reprompt = in.Reprompt
		out.Revision = rev
		out.UpdatedAt = now
		return nil
	})
	if err != nil {
		return Item{}, err
	}
	return out, nil
}

// SoftDeleteItem tombstones an item and hard-deletes its attachment rows in the
// same transaction. It returns the blob paths of the removed attachments so the
// caller can purge the underlying blobs after the commit succeeds. Attachments
// do not participate in the sync cursor, so dropping the rows directly (rather
// than tombstoning them) is correct and prevents orphaned/leaked blobs.
func (r *Repository) SoftDeleteItem(ctx context.Context, userID, id string, ifRevision int64) ([]string, error) {
	var blobPaths []string
	err := inTx(ctx, r.db, func(tx *sql.Tx) error {
		cur, err := lockItemForWrite(ctx, tx, userID, id)
		if err != nil {
			return err
		}
		if cur.Revision != ifRevision {
			c := cur
			return &ConflictError{CurrentRow: &c}
		}
		// Collect attachment blob paths before deleting the rows so the caller
		// can purge the blobs post-commit.
		rows, err := tx.QueryContext(ctx,
			`SELECT blob_path FROM vault_attachments WHERE item_id = ?`, id)
		if err != nil {
			return fmt.Errorf("vault: read attachment paths: %w", err)
		}
		for rows.Next() {
			var p string
			if err := rows.Scan(&p); err != nil {
				rows.Close()
				return err
			}
			blobPaths = append(blobPaths, p)
		}
		if err := rows.Close(); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM vault_attachments WHERE item_id = ?`, id); err != nil {
			return fmt.Errorf("vault: delete attachments: %w", err)
		}
		rev, err := bumpRev(ctx, tx, userID)
		if err != nil {
			return err
		}
		now := time.Now().UTC().Truncate(time.Second)
		res, err := tx.ExecContext(ctx, `
			UPDATE vault_items
			SET deleted_at = ?, revision = ?, updated_at = ?
			WHERE id = ? AND user_id = ? AND deleted_at IS NULL
		`, now, rev, now, id, userID)
		if err != nil {
			return fmt.Errorf("vault: soft-delete item: %w", err)
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return blobPaths, nil
}

// SyncResult is the payload returned to the client for a delta sync.
type SyncResult struct {
	Cursor  int64
	Folders []Folder
	Items   []Item
	HasMore bool
}

// Sync returns the folders and items changed since the cursor. When limit > 0,
// it pages across one merged revision stream, not per table, so a high-revision
// row from one table can never advance the cursor past unreturned lower
// revisions from the other table. Rows sharing one revision are kept together.
func (r *Repository) Sync(ctx context.Context, userID string, since, limit int64) (SyncResult, error) {
	cursor, err := r.currentRev(ctx, userID)
	if err != nil {
		return SyncResult{}, fmt.Errorf("vault: read cursor: %w", err)
	}
	res := SyncResult{Cursor: cursor}

	if limit > 0 {
		changes, nextCursor, hasMore, err := r.syncChanges(ctx, userID, since, cursor, limit)
		if err != nil {
			return SyncResult{}, err
		}
		res.Cursor = nextCursor
		res.HasMore = hasMore
		for _, ch := range changes {
			switch ch.Kind {
			case "folder":
				row := r.db.QueryRowContext(ctx, folderSelect+` WHERE id = ? AND user_id = ?`, ch.ID, userID)
				f, err := scanFolder(row)
				if err != nil {
					return SyncResult{}, err
				}
				res.Folders = append(res.Folders, f)
			case "item":
				row := r.db.QueryRowContext(ctx, itemSelect+` WHERE id = ? AND user_id = ?`, ch.ID, userID)
				it, err := scanItem(row)
				if err != nil {
					return SyncResult{}, err
				}
				res.Items = append(res.Items, it)
			}
		}
		return res, nil
	}

	rows, err := r.db.QueryContext(ctx, folderSelect+`
		WHERE user_id = ? AND revision > ? ORDER BY revision ASC`, userID, since)
	if err != nil {
		return SyncResult{}, fmt.Errorf("vault: sync folders: %w", err)
	}
	for rows.Next() {
		f, err := scanFolder(rows)
		if err != nil {
			rows.Close()
			return SyncResult{}, err
		}
		res.Folders = append(res.Folders, f)
	}
	if err := rows.Close(); err != nil {
		return SyncResult{}, err
	}

	rows, err = r.db.QueryContext(ctx, itemSelect+`
		WHERE user_id = ? AND revision > ? ORDER BY revision ASC`, userID, since)
	if err != nil {
		return SyncResult{}, fmt.Errorf("vault: sync items: %w", err)
	}
	for rows.Next() {
		it, err := scanItem(rows)
		if err != nil {
			rows.Close()
			return SyncResult{}, err
		}
		res.Items = append(res.Items, it)
	}
	if err := rows.Close(); err != nil {
		return SyncResult{}, err
	}
	return res, rows.Err()
}

type syncChange struct {
	Kind     string
	ID       string
	Revision int64
}

func (r *Repository) syncChanges(ctx context.Context, userID string, since, current, limit int64) ([]syncChange, int64, bool, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT kind, id, revision FROM (
			SELECT 'folder' AS kind, id, revision FROM vault_folders WHERE user_id = ? AND revision > ?
			UNION ALL
			SELECT 'item' AS kind, id, revision FROM vault_items WHERE user_id = ? AND revision > ?
		)
		ORDER BY revision ASC, kind ASC, id ASC
		LIMIT ?`, userID, since, userID, since, limit+1)
	if err != nil {
		return nil, 0, false, fmt.Errorf("vault: sync change stream: %w", err)
	}
	defer rows.Close()
	var changes []syncChange
	for rows.Next() {
		var ch syncChange
		if err := rows.Scan(&ch.Kind, &ch.ID, &ch.Revision); err != nil {
			return nil, 0, false, err
		}
		changes = append(changes, ch)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, false, err
	}
	if len(changes) == 0 {
		return nil, current, false, nil
	}
	if int64(len(changes)) <= limit {
		return changes, current, false, nil
	}

	page := changes[:limit]
	overflowRev := changes[limit].Revision
	lastRev := page[len(page)-1].Revision
	if lastRev != overflowRev {
		return page, lastRev, true, nil
	}

	cut := len(page)
	for cut > 0 && page[cut-1].Revision == overflowRev {
		cut--
	}
	if cut > 0 {
		return page[:cut], page[cut-1].Revision, true, nil
	}
	sameRev, err := r.syncChangesAtRevision(ctx, userID, overflowRev)
	if err != nil {
		return nil, 0, false, err
	}
	return sameRev, overflowRev, current > overflowRev, nil
}

func (r *Repository) syncChangesAtRevision(ctx context.Context, userID string, revision int64) ([]syncChange, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT kind, id, revision FROM (
			SELECT 'folder' AS kind, id, revision FROM vault_folders WHERE user_id = ? AND revision = ?
			UNION ALL
			SELECT 'item' AS kind, id, revision FROM vault_items WHERE user_id = ? AND revision = ?
		)
		ORDER BY kind ASC, id ASC`, userID, revision, userID, revision)
	if err != nil {
		return nil, fmt.Errorf("vault: sync revision group: %w", err)
	}
	defer rows.Close()
	var changes []syncChange
	for rows.Next() {
		var ch syncChange
		if err := rows.Scan(&ch.Kind, &ch.ID, &ch.Revision); err != nil {
			return nil, err
		}
		changes = append(changes, ch)
	}
	return changes, rows.Err()
}

// ListItemRevisions returns the archived history snapshots of an item, newest
// first, scoped to the owning user via a join.
func (r *Repository) ListItemRevisions(ctx context.Context, userID, itemID string) ([]ItemRevision, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT r.id, r.item_id, COALESCE(r.type, i.type), COALESCE(r.folder_id, ''),
		       r.name_cipher, r.name_nonce, r.data_cipher, r.data_nonce,
		       COALESCE(r.notes_cipher, ''), COALESCE(r.notes_nonce, ''),
		       COALESCE(r.crypto_version, i.crypto_version),
		       COALESCE(r.favorite, i.favorite), COALESCE(r.reprompt, i.reprompt),
		       r.revision, r.created_at
		FROM vault_item_revisions r
		JOIN vault_items i ON i.id = r.item_id
		WHERE i.user_id = ? AND r.item_id = ?
		ORDER BY r.created_at DESC`, userID, itemID)
	if err != nil {
		return nil, fmt.Errorf("vault: list item revisions: %w", err)
	}
	defer rows.Close()
	var out []ItemRevision
	for rows.Next() {
		var rev ItemRevision
		var typ string
		var fav, rep int
		if err := rows.Scan(&rev.ID, &rev.ItemID, &typ, &rev.FolderID,
			&rev.NameCipher, &rev.NameNonce, &rev.DataCipher, &rev.DataNonce,
			&rev.NotesCipher, &rev.NotesNonce, &rev.CryptoVersion, &fav, &rep, &rev.Revision, &rev.CreatedAt); err != nil {
			return nil, err
		}
		rev.Type = ItemType(typ)
		rev.Favorite = fav == 1
		rev.Reprompt = rep == 1
		out = append(out, rev)
	}
	return out, rows.Err()
}

// GetItemRevision returns one archived snapshot, ownership-scoped.
func (r *Repository) GetItemRevision(ctx context.Context, userID, itemID, revID string) (ItemRevision, error) {
	var rev ItemRevision
	row := r.db.QueryRowContext(ctx, `
		SELECT r.id, r.item_id, COALESCE(r.type, i.type), COALESCE(r.folder_id, ''),
		       r.name_cipher, r.name_nonce, r.data_cipher, r.data_nonce,
		       COALESCE(r.notes_cipher, ''), COALESCE(r.notes_nonce, ''),
		       COALESCE(r.crypto_version, i.crypto_version),
		       COALESCE(r.favorite, i.favorite), COALESCE(r.reprompt, i.reprompt),
		       r.revision, r.created_at
		FROM vault_item_revisions r
		JOIN vault_items i ON i.id = r.item_id
		WHERE i.user_id = ? AND r.item_id = ? AND r.id = ?`, userID, itemID, revID,
	)
	if err := scanItemRevision(row, &rev); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ItemRevision{}, ErrNotFound
		}
		return ItemRevision{}, err
	}
	return rev, nil
}

// RestoreItemRevision archives the current row, then overwrites it with the
// encrypted fields and metadata from an archived snapshot. Ownership is
// enforced via the item lock; ifRevision guards against clobbering a concurrent
// edit. The snapshot must belong to the item.
func (r *Repository) RestoreItemRevision(ctx context.Context, userID, itemID, revID string, ifRevision int64) (Item, error) {
	var out Item
	err := inTx(ctx, r.db, func(tx *sql.Tx) error {
		cur, err := lockItemForWrite(ctx, tx, userID, itemID)
		if err != nil {
			return err
		}
		if cur.Revision != ifRevision {
			c := cur
			return &ConflictError{CurrentRow: &c}
		}
		var snap ItemRevision
		row := tx.QueryRowContext(ctx, `
			SELECT r.id, r.item_id, COALESCE(r.type, i.type), COALESCE(r.folder_id, ''),
			       r.name_cipher, r.name_nonce, r.data_cipher, r.data_nonce,
			       COALESCE(r.notes_cipher, ''), COALESCE(r.notes_nonce, ''),
			       COALESCE(r.crypto_version, i.crypto_version),
			       COALESCE(r.favorite, i.favorite), COALESCE(r.reprompt, i.reprompt),
			       r.revision, r.created_at
			FROM vault_item_revisions r
			JOIN vault_items i ON i.id = r.item_id
			WHERE i.user_id = ? AND r.item_id = ? AND r.id = ?`,
			userID, itemID, revID,
		)
		if err := scanItemRevision(row, &snap); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		if err := archiveItemSnapshot(ctx, tx, cur); err != nil {
			return fmt.Errorf("vault: archive before restore: %w", err)
		}
		rev, err := bumpRev(ctx, tx, userID)
		if err != nil {
			return err
		}
		now := time.Now().UTC().Truncate(time.Second)
		folderID, err := r.restoreFolderID(ctx, tx, userID, snap.FolderID)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE vault_items SET
			  type = ?, folder_id = ?, name_cipher = ?, name_nonce = ?,
			  data_cipher = ?, data_nonce = ?, notes_cipher = ?, notes_nonce = ?,
			  crypto_version = ?, favorite = ?, reprompt = ?, revision = ?, updated_at = ?
			WHERE id = ? AND user_id = ? AND deleted_at IS NULL`,
			string(snap.Type), nullable(folderID), snap.NameCipher, snap.NameNonce,
			snap.DataCipher, snap.DataNonce, nullable(snap.NotesCipher), nullable(snap.NotesNonce),
			snap.CryptoVersion, boolToInt(snap.Favorite), boolToInt(snap.Reprompt), rev, now, itemID, userID); err != nil {
			return fmt.Errorf("vault: restore item: %w", err)
		}
		out = cur
		out.Type = snap.Type
		out.FolderID = folderID
		out.NameCipher = snap.NameCipher
		out.NameNonce = snap.NameNonce
		out.DataCipher = snap.DataCipher
		out.DataNonce = snap.DataNonce
		out.NotesCipher = snap.NotesCipher
		out.NotesNonce = snap.NotesNonce
		out.CryptoVersion = snap.CryptoVersion
		out.Favorite = snap.Favorite
		out.Reprompt = snap.Reprompt
		out.Revision = rev
		out.UpdatedAt = now
		return nil
	})
	if err != nil {
		return Item{}, err
	}
	return out, nil
}

func (r *Repository) restoreFolderID(ctx context.Context, tx *sql.Tx, userID, folderID string) (string, error) {
	if folderID == "" {
		return "", nil
	}
	if err := ensureFolderOwned(ctx, tx, userID, folderID); err != nil {
		if errors.Is(err, ErrNotFound) {
			return "", nil
		}
		return "", err
	}
	return folderID, nil
}

func archiveItemSnapshot(ctx context.Context, tx *sql.Tx, it Item) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO vault_item_revisions
		  (id, item_id, type, folder_id, name_cipher, name_nonce, data_cipher, data_nonce,
		   notes_cipher, notes_nonce, crypto_version, favorite, reprompt, revision, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id.New(), it.ID, string(it.Type), nullable(it.FolderID),
		it.NameCipher, it.NameNonce, it.DataCipher, it.DataNonce,
		nullable(it.NotesCipher), nullable(it.NotesNonce),
		it.CryptoVersion,
		boolToInt(it.Favorite), boolToInt(it.Reprompt), it.Revision, it.UpdatedAt)
	return err
}

// --- import -----------------------------------------------------------------

// ImportBundle re-inserts folders and items with fresh IDs and bumped
// revisions, returning the count of each. The input is ciphertext the client
// prepared (possibly re-encrypted for this vault). Tombstones are preserved as
// tombstones so a restore round-trip is faithful.
func (r *Repository) ImportBundle(ctx context.Context, userID string, folders []Folder, items []Item, attachments []Attachment) (folderCount, itemCount, attachmentCount int64, err error) {
	err = inTx(ctx, r.db, func(tx *sql.Tx) error {
		now := time.Now().UTC().Truncate(time.Second)
		folderIDs := make(map[string]string, len(folders))
		itemIDs := make(map[string]string, len(items))
		for _, f := range folders {
			oldID := f.ID
			rev, err := bumpRev(ctx, tx, userID)
			if err != nil {
				return err
			}
			f.ID = id.New()
			f.UserID = userID
			f.Revision = rev
			f.CreatedAt = now
			f.UpdatedAt = now
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO vault_folders
				  (id, user_id, name_cipher, name_nonce, crypto_version, revision, created_at, updated_at, deleted_at)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				f.ID, f.UserID, f.NameCipher, f.NameNonce, f.CryptoVersion, rev, now, now,
				nullableTime(f.DeletedAt)); err != nil {
				return fmt.Errorf("vault: import folder: %w", err)
			}
			if oldID != "" {
				folderIDs[oldID] = f.ID
			}
			folderCount++
		}
		for _, it := range items {
			oldID := it.ID
			if !it.Type.Valid() {
				return fmt.Errorf("%w: invalid item type in import bundle", ErrInvalidInput)
			}
			rev, err := bumpRev(ctx, tx, userID)
			if err != nil {
				return err
			}
			it.ID = id.New()
			it.UserID = userID
			if it.FolderID != "" {
				if mapped, ok := folderIDs[it.FolderID]; ok {
					it.FolderID = mapped
				} else {
					it.FolderID = ""
				}
			}
			it.Revision = rev
			it.CreatedAt = now
			it.UpdatedAt = now
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO vault_items
				  (id, user_id, type, folder_id, name_cipher, name_nonce,
				   data_cipher, data_nonce, notes_cipher, notes_nonce,
				   crypto_version, favorite, reprompt, revision, created_at, updated_at, deleted_at)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				it.ID, it.UserID, string(it.Type), nullable(it.FolderID),
				it.NameCipher, it.NameNonce, it.DataCipher, it.DataNonce,
				nullable(it.NotesCipher), nullable(it.NotesNonce),
				it.CryptoVersion,
				boolToInt(it.Favorite), boolToInt(it.Reprompt), rev, now, now,
				nullableTime(it.DeletedAt)); err != nil {
				return fmt.Errorf("vault: import item: %w", err)
			}
			if oldID != "" {
				itemIDs[oldID] = it.ID
			}
			itemCount++
		}
		for _, a := range attachments {
			itemID, ok := itemIDs[a.ItemID]
			if !ok {
				return fmt.Errorf("%w: attachment references missing imported item", ErrInvalidInput)
			}
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO vault_attachments
				  (id, item_id, blob_path, size_bytes,
				   file_key_cipher, file_key_nonce, name_cipher, name_nonce, crypto_version, created_at)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				id.New(), itemID, a.BlobPath, a.SizeBytes,
				a.FileKeyCipher, a.FileKeyNonce, a.NameCipher, a.NameNonce, a.CryptoVersion, now); err != nil {
				return fmt.Errorf("vault: import attachment: %w", err)
			}
			attachmentCount++
		}
		return nil
	})
	return folderCount, itemCount, attachmentCount, err
}

// --- attachments -----------------------------------------------------------
//
// Attachments are scoped through vault_items.user_id: every query joins the
// item so a user can only touch attachments on items they own. They do not
// participate in the per-user sync cursor — the SPA fetches them per item.

const attachmentSelect = `
	SELECT a.id, a.item_id, a.blob_path, a.size_bytes,
	       a.file_key_cipher, a.file_key_nonce,
	       a.name_cipher, a.name_nonce, a.crypto_version, a.created_at
	FROM vault_attachments a
	JOIN vault_items i ON i.id = a.item_id
	WHERE i.user_id = ? AND a.item_id = ? AND i.deleted_at IS NULL`

// CreateAttachment records a new attachment row pointing at blobPath.
func (r *Repository) CreateAttachment(ctx context.Context, userID, itemID, blobPath string,
	sizeBytes int64, fkCipher, fkNonce, nameCipher, nameNonce string, cryptoVersion int,
) (Attachment, error) {
	a := Attachment{ID: id.New(), ItemID: itemID, BlobPath: blobPath, SizeBytes: sizeBytes,
		FileKeyCipher: fkCipher, FileKeyNonce: fkNonce, NameCipher: nameCipher, NameNonce: nameNonce, CryptoVersion: cryptoVersion}
	now := time.Now().UTC().Truncate(time.Second)
	a.CreatedAt = now
	res, err := r.db.ExecContext(ctx, `
		INSERT INTO vault_attachments
		  (id, item_id, blob_path, size_bytes,
		   file_key_cipher, file_key_nonce, name_cipher, name_nonce, crypto_version, created_at)
		SELECT ?, ?, ?, ?, ?, ?, ?, ?, ?, ?
		WHERE EXISTS (SELECT 1 FROM vault_items WHERE id = ? AND user_id = ? AND deleted_at IS NULL)
	`,
		a.ID, a.ItemID, a.BlobPath, a.SizeBytes,
		a.FileKeyCipher, a.FileKeyNonce, a.NameCipher, a.NameNonce, a.CryptoVersion, now,
		itemID, userID,
	)
	if err != nil {
		return Attachment{}, fmt.Errorf("vault: insert attachment: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return Attachment{}, ErrNotFound
	}
	return a, nil
}

// ListAttachments returns all attachments for an item owned by userID.
func (r *Repository) ListAttachments(ctx context.Context, userID, itemID string) ([]Attachment, error) {
	rows, err := r.db.QueryContext(ctx, attachmentSelect+` ORDER BY a.created_at ASC`, userID, itemID)
	if err != nil {
		return nil, fmt.Errorf("vault: list attachments: %w", err)
	}
	defer rows.Close()
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

// ListAllAttachments returns every attachment for a user's non-deleted items.
func (r *Repository) ListAllAttachments(ctx context.Context, userID string) ([]Attachment, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT a.id, a.item_id, a.blob_path, a.size_bytes,
		       a.file_key_cipher, a.file_key_nonce,
		       a.name_cipher, a.name_nonce, a.crypto_version, a.created_at
		FROM vault_attachments a
		JOIN vault_items i ON i.id = a.item_id
		WHERE i.user_id = ? AND i.deleted_at IS NULL
		ORDER BY a.created_at ASC`, userID)
	if err != nil {
		return nil, fmt.Errorf("vault: list all attachments: %w", err)
	}
	defer rows.Close()
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

// GetAttachment returns one attachment, enforcing ownership via the item join.
func (r *Repository) GetAttachment(ctx context.Context, userID, itemID, attachID string) (Attachment, error) {
	row := r.db.QueryRowContext(ctx, attachmentSelect+` AND a.id = ?`, userID, itemID, attachID)
	a, err := scanAttachment(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Attachment{}, ErrNotFound
	}
	return a, err
}

// DeleteAttachment removes the attachment row (ownership-scoped). It returns
// ErrNotFound if the attachment or the owning item does not exist.
func (r *Repository) DeleteAttachment(ctx context.Context, userID, itemID, attachID string) error {
	res, err := r.db.ExecContext(ctx, `
		DELETE FROM vault_attachments
		WHERE id = ? AND item_id = ?
		  AND EXISTS (SELECT 1 FROM vault_items WHERE id = ? AND user_id = ?)
	`, attachID, itemID, itemID, userID)
	if err != nil {
		return fmt.Errorf("vault: delete attachment: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// --- janitor ----------------------------------------------------------------

// PurgeOldTombstones hard-deletes folders and items soft-deleted before the
// cutoff, along with their archived history. This does NOT bump the sync
// cursor (tombstone purge is invisible to clients — they already saw the
// tombstone and dropped the row locally). It returns the number of folders and
// item rows removed.
func (r *Repository) PurgeOldTombstones(ctx context.Context, olderThan time.Time) (folders, items int64, err error) {
	// Hard-deleting items cascades to vault_item_revisions and (now empty)
	// vault_attachments via FK ON DELETE CASCADE.
	res, err := r.db.ExecContext(ctx,
		`DELETE FROM vault_items WHERE deleted_at IS NOT NULL AND deleted_at < ?`, olderThan)
	if err != nil {
		return 0, 0, fmt.Errorf("vault: purge item tombstones: %w", err)
	}
	items, _ = res.RowsAffected()
	res, err = r.db.ExecContext(ctx,
		`DELETE FROM vault_folders WHERE deleted_at IS NOT NULL AND deleted_at < ?`, olderThan)
	if err != nil {
		return items, 0, fmt.Errorf("vault: purge folder tombstones: %w", err)
	}
	folders, _ = res.RowsAffected()
	return folders, items, nil
}

// AllAttachmentBlobPaths returns every blob_path stored in vault_attachments.
// The janitor diffs this set against the blob store listing to find and delete
// orphaned blobs (e.g. from a crash between Save and the DB insert, or a failed
// blob Delete after a row was removed).
func (r *Repository) AllAttachmentBlobPaths(ctx context.Context) ([]string, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT blob_path FROM vault_attachments`)
	if err != nil {
		return nil, fmt.Errorf("vault: read attachment blob paths: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func scanAttachment(row scanner) (Attachment, error) {
	var a Attachment
	err := row.Scan(
		&a.ID, &a.ItemID, &a.BlobPath, &a.SizeBytes,
		&a.FileKeyCipher, &a.FileKeyNonce,
		&a.NameCipher, &a.NameNonce, &a.CryptoVersion, &a.CreatedAt,
	)
	if err != nil {
		return Attachment{}, err
	}
	return a, nil
}

// inTx runs fn inside a database transaction, committing on nil error.
func inTx(ctx context.Context, db *sql.DB, fn func(*sql.Tx) error) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("vault: begin tx: %w", err)
	}
	defer tx.Rollback()
	if err := fn(tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("vault: commit: %w", err)
	}
	return nil
}

// lockFolderForWrite reads a folder row within tx. SQLite's default deferred
// transaction plus single-writer serialization is enough for correctness here.
func lockFolderForWrite(ctx context.Context, tx *sql.Tx, userID, id string) (Folder, error) {
	row := tx.QueryRowContext(ctx, folderSelect+` WHERE id = ? AND user_id = ?`, id, userID)
	f, err := scanFolder(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Folder{}, ErrNotFound
	}
	if err != nil {
		return Folder{}, err
	}
	if f.DeletedAt != nil {
		return Folder{}, ErrNotFound
	}
	return f, nil
}

func lockItemForWrite(ctx context.Context, tx *sql.Tx, userID, id string) (Item, error) {
	row := tx.QueryRowContext(ctx, itemSelect+` WHERE id = ? AND user_id = ?`, id, userID)
	it, err := scanItem(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Item{}, ErrNotFound
	}
	if err != nil {
		return Item{}, err
	}
	if it.DeletedAt != nil {
		return Item{}, ErrNotFound
	}
	return it, nil
}

func ensureFolderOwned(ctx context.Context, tx *sql.Tx, userID, folderID string) error {
	if folderID == "" {
		return nil
	}
	var ok int
	err := tx.QueryRowContext(ctx,
		`SELECT 1 FROM vault_folders WHERE id = ? AND user_id = ? AND deleted_at IS NULL`,
		folderID, userID,
	).Scan(&ok)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("vault: verify folder ownership: %w", err)
	}
	return nil
}

const folderSelect = `
	SELECT id, user_id, name_cipher, name_nonce, crypto_version, revision,
	       created_at, updated_at, deleted_at
	FROM vault_folders`

const itemSelect = `
	SELECT id, user_id, type, COALESCE(folder_id,''),
	       name_cipher, name_nonce, data_cipher, data_nonce,
	       COALESCE(notes_cipher,''), COALESCE(notes_nonce,''),
	       crypto_version, favorite, reprompt, revision, created_at, updated_at, deleted_at
	FROM vault_items`

type scanner interface {
	Scan(dest ...any) error
}

func scanFolder(row scanner) (Folder, error) {
	var f Folder
	var deleted sql.NullTime
	err := row.Scan(&f.ID, &f.UserID, &f.NameCipher, &f.NameNonce, &f.CryptoVersion, &f.Revision,
		&f.CreatedAt, &f.UpdatedAt, &deleted)
	if err != nil {
		return Folder{}, err
	}
	if deleted.Valid {
		t := deleted.Time.UTC()
		f.DeletedAt = &t
	}
	return f, nil
}

func scanItem(row scanner) (Item, error) {
	var it Item
	var typ string
	var fav, rep int
	var deleted sql.NullTime
	err := row.Scan(&it.ID, &it.UserID, &typ, &it.FolderID,
		&it.NameCipher, &it.NameNonce, &it.DataCipher, &it.DataNonce,
		&it.NotesCipher, &it.NotesNonce, &it.CryptoVersion, &fav, &rep,
		&it.Revision, &it.CreatedAt, &it.UpdatedAt, &deleted)
	if err != nil {
		return Item{}, err
	}
	it.Type = ItemType(typ)
	it.Favorite = fav == 1
	it.Reprompt = rep == 1
	if deleted.Valid {
		t := deleted.Time.UTC()
		it.DeletedAt = &t
	}
	return it, nil
}

func scanItemRevision(row scanner, rev *ItemRevision) error {
	var typ string
	var fav, rep int
	if err := row.Scan(&rev.ID, &rev.ItemID, &typ, &rev.FolderID,
		&rev.NameCipher, &rev.NameNonce, &rev.DataCipher, &rev.DataNonce,
		&rev.NotesCipher, &rev.NotesNonce, &rev.CryptoVersion, &fav, &rep, &rev.Revision, &rev.CreatedAt); err != nil {
		return err
	}
	rev.Type = ItemType(typ)
	rev.Favorite = fav == 1
	rev.Reprompt = rep == 1
	return nil
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// nullableTime returns the SQL representation of a *time.Time (NULL when nil).
func nullableTime(t *time.Time) any {
	if t == nil {
		return nil
	}
	return t.UTC()
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// isUniqueViolation reports whether err is a SQLite UNIQUE/PK constraint
// failure. modernc.org/sqlite prefixes constraint errors with "constraint
// failed"; we match on the UNIQUE-specific phrase so that a FOREIGN KEY
// constraint failure (also prefixed "constraint failed") is NOT mistaken for a
// duplicate-key conflict — otherwise setup against a missing user would be
// misreported as "envelope already exists".
func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	// modernc returns errors like:
	//   "constraint failed: UNIQUE constraint failed: vault_keys.user_id (1555)"
	return strings.Contains(msg, "UNIQUE constraint failed")
}
