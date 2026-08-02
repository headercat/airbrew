package vault

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
)

// Service contains vault business logic. It never sees plaintext: input is
// validated for shape only (non-empty ciphertext/nonces) and forwarded to the
// repository.
type Service struct {
	repo *Repository
}

// NewService returns a Service backed by repo.
func NewService(repo *Repository) *Service { return &Service{repo: repo} }

// Setup stores the initial key envelope for userID. Returns ErrEnvelopeExists
// if the vault was already initialized.
func (s *Service) Setup(ctx context.Context, env KeyEnvelope) error {
	if err := validateEnvelope(&env); err != nil {
		return err
	}
	if err := s.repo.SetupEnvelope(ctx, env); err != nil {
		return err
	}
	return nil
}

// GetEnvelope returns the key envelope for client-side unlock.
func (s *Service) GetEnvelope(ctx context.Context, userID string) (KeyEnvelope, error) {
	return s.repo.GetEnvelope(ctx, userID)
}

// RotateEnvelope replaces the protected vault-key wrapper after a master
// password change. The vault key itself is unchanged, so no items are touched.
//
// ifVersion is the envelope Version the client last observed. The rotation only
// applies if it matches the stored version; otherwise a *ConflictError is
// returned carrying the server's current envelope so the client can re-fetch
// and retry. This prevents two concurrent rotations from silently clobbering
// one another and locking a device out of its vault.
func (s *Service) RotateEnvelope(ctx context.Context, env KeyEnvelope, ifVersion int64) error {
	if err := validateEnvelope(&env); err != nil {
		return err
	}
	if ifVersion <= 0 {
		return fmt.Errorf("%w: if_version required", ErrInvalidInput)
	}
	return s.repo.RotateEnvelope(ctx, env, ifVersion)
}

// CreateFolder creates an encrypted folder.
func (s *Service) CreateFolder(ctx context.Context, userID, nameCipher, nameNonce string) (Folder, error) {
	if nameCipher == "" || nameNonce == "" {
		return Folder{}, fmt.Errorf("%w: name ciphertext and nonce required", ErrInvalidInput)
	}
	if err := validateCipherPair(nameCipher, nameNonce, "name"); err != nil {
		return Folder{}, err
	}
	return s.repo.CreateFolder(ctx, userID, nameCipher, nameNonce)
}

// UpdateFolder renames a folder, guarding on the client's last-seen revision.
func (s *Service) UpdateFolder(ctx context.Context, userID, id, nameCipher, nameNonce string, ifRevision int64) (Folder, error) {
	if nameCipher == "" || nameNonce == "" {
		return Folder{}, fmt.Errorf("%w: name ciphertext and nonce required", ErrInvalidInput)
	}
	if err := validateCipherPair(nameCipher, nameNonce, "name"); err != nil {
		return Folder{}, err
	}
	if ifRevision <= 0 {
		return Folder{}, fmt.Errorf("%w: if_revision required", ErrInvalidInput)
	}
	return s.repo.UpdateFolder(ctx, userID, id, nameCipher, nameNonce, ifRevision)
}

// DeleteFolder soft-deletes a folder.
func (s *Service) DeleteFolder(ctx context.Context, userID, id string, ifRevision int64) error {
	if ifRevision <= 0 {
		return fmt.Errorf("%w: if_revision required", ErrInvalidInput)
	}
	return s.repo.SoftDeleteFolder(ctx, userID, id, ifRevision)
}

// CreateItem stores a new encrypted item.
func (s *Service) CreateItem(ctx context.Context, userID string, in ItemInput) (Item, error) {
	if err := validateItem(in); err != nil {
		return Item{}, err
	}
	return s.repo.CreateItem(ctx, userID, in)
}

// GetItem returns one item.
func (s *Service) GetItem(ctx context.Context, userID, id string) (Item, error) {
	return s.repo.GetItem(ctx, userID, id)
}

// UpdateItem overwrites an item, archiving the prior snapshot.
func (s *Service) UpdateItem(ctx context.Context, userID, id string, in ItemInput, ifRevision int64) (Item, error) {
	if err := validateItem(in); err != nil {
		return Item{}, err
	}
	if ifRevision <= 0 {
		return Item{}, fmt.Errorf("%w: if_revision required", ErrInvalidInput)
	}
	return s.repo.UpdateItem(ctx, userID, id, in, ifRevision)
}

// DeleteItem soft-deletes an item and returns the blob paths of any attachments
// that were dropped in the same transaction, so the caller can purge them from
// the blob store.
func (s *Service) DeleteItem(ctx context.Context, userID, id string, ifRevision int64) ([]string, error) {
	if ifRevision <= 0 {
		return nil, fmt.Errorf("%w: if_revision required", ErrInvalidInput)
	}
	return s.repo.SoftDeleteItem(ctx, userID, id, ifRevision)
}

// Sync returns delta (or full, when since=0) changes since the cursor. When
// limit > 0, at most that many folders and items are returned per call and
// HasMore signals the client to continue from the returned Cursor.
func (s *Service) Sync(ctx context.Context, userID string, since, limit int64) (SyncResult, error) {
	if since < 0 {
		since = 0
	}
	if limit < 0 {
		limit = 0
	}
	return s.repo.Sync(ctx, userID, since, limit)
}

// ListItemRevisions returns the archived history of an item, newest first.
func (s *Service) ListItemRevisions(ctx context.Context, userID, itemID string) ([]ItemRevision, error) {
	return s.repo.ListItemRevisions(ctx, userID, itemID)
}

// RestoreItemRevision re-stamps an archived snapshot as the current item. The
// server already holds the snapshot's ciphertext, so no re-encryption is
// needed: the current row is archived and then overwritten with the snapshot's
// encrypted fields and metadata, guarded by ifRevision.
func (s *Service) RestoreItemRevision(ctx context.Context, userID, itemID, revID string, ifRevision int64) (Item, error) {
	if ifRevision <= 0 {
		return Item{}, fmt.Errorf("%w: if_revision required", ErrInvalidInput)
	}
	return s.repo.RestoreItemRevision(ctx, userID, itemID, revID, ifRevision)
}

func validateEnvelope(env *KeyEnvelope) error {
	if env.UserID == "" {
		return fmt.Errorf("%w: user_id required", ErrInvalidInput)
	}
	if env.KDFAlgorithm == "" {
		env.KDFAlgorithm = "argon2id"
	}
	if env.KDFAlgorithm != "argon2id" {
		return fmt.Errorf("%w: unsupported kdf_algorithm", ErrInvalidInput)
	}
	if env.KDFSalt == "" {
		return fmt.Errorf("%w: kdf_salt required", ErrInvalidInput)
	}
	if env.ProtectedVaultKey == "" || env.ProtectedVaultNonce == "" {
		return fmt.Errorf("%w: protected_vault_key and nonce required", ErrInvalidInput)
	}
	if _, err := decodeB64Exact(env.KDFSalt, 16, "kdf_salt"); err != nil {
		return err
	}
	if err := validateCipherBlob(env.ProtectedVaultKey, "protected_vault_key"); err != nil {
		return err
	}
	if err := validateNonce(env.ProtectedVaultNonce, "protected_vault_nonce"); err != nil {
		return err
	}
	if env.KDFMemoryKiB <= 0 || env.KDFIterations <= 0 || env.KDFParallelism <= 0 {
		return fmt.Errorf("%w: kdf params must be positive", ErrInvalidInput)
	}
	if env.KDFMemoryKiB < 65536 || env.KDFIterations < 3 || env.KDFParallelism < 1 {
		return fmt.Errorf("%w: kdf params below minimum", ErrInvalidInput)
	}
	// Upper-bound the KDF cost so a malicious client cannot register an
	// envelope whose unlock would pin multi-gigabyte memory for minutes.
	// 1 GiB / 100 iterations / 64 lanes is well above any legitimate setting
	// (OWASP argon2id guidance is ~19 MiB / t=2 / p=1).
	if env.KDFMemoryKiB > 1<<20 || env.KDFIterations > 100 || env.KDFParallelism > 64 {
		return fmt.Errorf("%w: kdf params exceed upper bound", ErrInvalidInput)
	}
	return nil
}

func validateItem(in ItemInput) error {
	if !in.Type.Valid() {
		return fmt.Errorf("%w: invalid type", ErrInvalidInput)
	}
	if in.NameCipher == "" || in.NameNonce == "" {
		return fmt.Errorf("%w: name ciphertext and nonce required", ErrInvalidInput)
	}
	if in.DataCipher == "" || in.DataNonce == "" {
		return fmt.Errorf("%w: data ciphertext and nonce required", ErrInvalidInput)
	}
	if err := validateCipherPair(in.NameCipher, in.NameNonce, "name"); err != nil {
		return err
	}
	if err := validateCipherPair(in.DataCipher, in.DataNonce, "data"); err != nil {
		return err
	}
	if in.NotesCipher != "" && in.NotesNonce == "" {
		return fmt.Errorf("%w: notes nonce required when notes ciphertext present", ErrInvalidInput)
	}
	if in.NotesCipher != "" {
		if err := validateCipherPair(in.NotesCipher, in.NotesNonce, "notes"); err != nil {
			return err
		}
	}
	return nil
}

const maxCiphertextBytes = 64 << 10

func validateCipherPair(cipher, nonce, field string) error {
	if err := validateCipherBlob(cipher, field+"_cipher"); err != nil {
		return err
	}
	return validateNonce(nonce, field+"_nonce")
}

func validateCipherBlob(value, field string) error {
	b, err := decodeB64(value, field)
	if err != nil {
		return err
	}
	if len(b) < 17 {
		return fmt.Errorf("%w: %s too short", ErrInvalidInput, field)
	}
	if len(b) > maxCiphertextBytes {
		return fmt.Errorf("%w: %s too large", ErrInvalidInput, field)
	}
	return nil
}

func validateNonce(value, field string) error {
	_, err := decodeB64Exact(value, 12, field)
	return err
}

func decodeB64Exact(value string, want int, field string) ([]byte, error) {
	b, err := decodeB64(value, field)
	if err != nil {
		return nil, err
	}
	if len(b) != want {
		return nil, fmt.Errorf("%w: %s must decode to %d bytes", ErrInvalidInput, field, want)
	}
	return b, nil
}

func decodeB64(value, field string) ([]byte, error) {
	b, err := base64.StdEncoding.Strict().DecodeString(value)
	if err != nil {
		return nil, fmt.Errorf("%w: %s must be base64", ErrInvalidInput, field)
	}
	return b, nil
}

// AsConflict returns the *ConflictError stored in err, or nil.
func AsConflict(err error) *ConflictError {
	var ce *ConflictError
	if errors.As(err, &ce) {
		return ce
	}
	return nil
}

// --- attachments -----------------------------------------------------------

// MaxAttachmentBytes is the upper bound on a single attachment's plaintext
// size. It mirrors the handler's ciphertext upload cap so a client cannot claim
// an implausible size_bytes (the server stores it verbatim otherwise). Kept in
// the domain package so tests can assert the contract without importing HTTP.
const MaxAttachmentBytes = 10 << 20 // 10 MiB

// CreateAttachment records an attachment row. The handler has already written
// the encrypted blob to the blob store; it passes the resulting path here.
func (s *Service) CreateAttachment(ctx context.Context, userID, itemID, blobPath string,
	sizeBytes int64, fkCipher, fkNonce, nameCipher, nameNonce string,
) (Attachment, error) {
	if blobPath == "" || fkCipher == "" || fkNonce == "" || nameCipher == "" || nameNonce == "" {
		return Attachment{}, fmt.Errorf("%w: attachment ciphertext required", ErrInvalidInput)
	}
	if err := validateCipherPair(fkCipher, fkNonce, "file_key"); err != nil {
		return Attachment{}, err
	}
	if err := validateCipherPair(nameCipher, nameNonce, "name"); err != nil {
		return Attachment{}, err
	}
	if sizeBytes < 0 {
		return Attachment{}, fmt.Errorf("%w: negative size", ErrInvalidInput)
	}
	if sizeBytes > MaxAttachmentBytes {
		return Attachment{}, fmt.Errorf("%w: attachment too large", ErrInvalidInput)
	}
	return s.repo.CreateAttachment(ctx, userID, itemID, blobPath, sizeBytes, fkCipher, fkNonce, nameCipher, nameNonce)
}

// ListAttachments returns all attachments for an item.
func (s *Service) ListAttachments(ctx context.Context, userID, itemID string) ([]Attachment, error) {
	return s.repo.ListAttachments(ctx, userID, itemID)
}

// GetAttachment returns one attachment (ownership-scoped via the item join).
func (s *Service) GetAttachment(ctx context.Context, userID, itemID, attachID string) (Attachment, error) {
	return s.repo.GetAttachment(ctx, userID, itemID, attachID)
}

// DeleteAttachment removes the attachment row. The handler is responsible for
// deleting the underlying blob afterwards.
func (s *Service) DeleteAttachment(ctx context.Context, userID, itemID, attachID string) error {
	return s.repo.DeleteAttachment(ctx, userID, itemID, attachID)
}

// ExportBundle returns the full encrypted vault (folders + items, including
// tombstones), attachments, and the key envelope, so the client can download a
// self-contained ciphertext backup. The server cannot decrypt any of it.
func (s *Service) ExportBundle(ctx context.Context, userID string) (KeyEnvelope, []Folder, []Item, []Attachment, error) {
	env, err := s.repo.GetEnvelope(ctx, userID)
	if err != nil {
		return KeyEnvelope{}, nil, nil, nil, err
	}
	res, err := s.repo.Sync(ctx, userID, 0, 0)
	if err != nil {
		return KeyEnvelope{}, nil, nil, nil, err
	}
	atts, err := s.repo.ListAllAttachments(ctx, userID)
	if err != nil {
		return KeyEnvelope{}, nil, nil, nil, err
	}
	return env, res.Folders, res.Items, atts, nil
}

// ImportBundle re-inserts the given ciphertext folders and items with fresh IDs
// and bumped revisions, returning the count of each.
func (s *Service) ImportBundle(ctx context.Context, userID string, folders []Folder, items []Item, attachments []Attachment) (int64, int64, int64, error) {
	for _, f := range folders {
		if f.NameCipher == "" || f.NameNonce == "" {
			return 0, 0, 0, fmt.Errorf("%w: folder name ciphertext required", ErrInvalidInput)
		}
		if err := validateCipherPair(f.NameCipher, f.NameNonce, "folder_name"); err != nil {
			return 0, 0, 0, err
		}
	}
	for _, it := range items {
		if err := validateItem(ItemInput{
			Type: it.Type, FolderID: it.FolderID,
			NameCipher: it.NameCipher, NameNonce: it.NameNonce,
			DataCipher: it.DataCipher, DataNonce: it.DataNonce,
			NotesCipher: it.NotesCipher, NotesNonce: it.NotesNonce,
			Favorite: it.Favorite, Reprompt: it.Reprompt,
		}); err != nil {
			return 0, 0, 0, err
		}
	}
	for _, a := range attachments {
		if a.ItemID == "" || a.BlobPath == "" {
			return 0, 0, 0, fmt.Errorf("%w: attachment item_id and blob_path required", ErrInvalidInput)
		}
		if a.SizeBytes < 0 || a.SizeBytes > MaxAttachmentBytes {
			return 0, 0, 0, fmt.Errorf("%w: invalid attachment size", ErrInvalidInput)
		}
		if err := validateCipherPair(a.FileKeyCipher, a.FileKeyNonce, "file_key"); err != nil {
			return 0, 0, 0, err
		}
		if err := validateCipherPair(a.NameCipher, a.NameNonce, "name"); err != nil {
			return 0, 0, 0, err
		}
	}
	return s.repo.ImportBundle(ctx, userID, folders, items, attachments)
}
