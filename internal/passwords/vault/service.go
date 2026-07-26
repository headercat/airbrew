package vault

import (
	"context"
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
	if err := validateEnvelope(env); err != nil {
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
func (s *Service) RotateEnvelope(ctx context.Context, env KeyEnvelope) error {
	if err := validateEnvelope(env); err != nil {
		return err
	}
	return s.repo.RotateEnvelope(ctx, env)
}

// CreateFolder creates an encrypted folder.
func (s *Service) CreateFolder(ctx context.Context, userID, nameCipher, nameNonce string) (Folder, error) {
	if nameCipher == "" || nameNonce == "" {
		return Folder{}, fmt.Errorf("%w: name ciphertext and nonce required", ErrInvalidInput)
	}
	return s.repo.CreateFolder(ctx, userID, nameCipher, nameNonce)
}

// UpdateFolder renames a folder, guarding on the client's last-seen revision.
func (s *Service) UpdateFolder(ctx context.Context, userID, id, nameCipher, nameNonce string, ifRevision int64) (Folder, error) {
	if nameCipher == "" || nameNonce == "" {
		return Folder{}, fmt.Errorf("%w: name ciphertext and nonce required", ErrInvalidInput)
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

// DeleteItem soft-deletes an item.
func (s *Service) DeleteItem(ctx context.Context, userID, id string, ifRevision int64) error {
	if ifRevision <= 0 {
		return fmt.Errorf("%w: if_revision required", ErrInvalidInput)
	}
	return s.repo.SoftDeleteItem(ctx, userID, id, ifRevision)
}

// Sync returns delta (or full, when since=0) changes since the cursor.
func (s *Service) Sync(ctx context.Context, userID string, since int64) (SyncResult, error) {
	if since < 0 {
		since = 0
	}
	return s.repo.Sync(ctx, userID, since)
}

func validateEnvelope(env KeyEnvelope) error {
	if env.UserID == "" {
		return fmt.Errorf("%w: user_id required", ErrInvalidInput)
	}
	if env.KDFAlgorithm == "" {
		env.KDFAlgorithm = "argon2id"
	}
	if env.KDFSalt == "" {
		return fmt.Errorf("%w: kdf_salt required", ErrInvalidInput)
	}
	if env.ProtectedVaultKey == "" || env.ProtectedVaultNonce == "" {
		return fmt.Errorf("%w: protected_vault_key and nonce required", ErrInvalidInput)
	}
	if env.KDFMemoryKiB <= 0 || env.KDFIterations <= 0 || env.KDFParallelism <= 0 {
		return fmt.Errorf("%w: kdf params must be positive", ErrInvalidInput)
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
	if in.NotesCipher != "" && in.NotesNonce == "" {
		return fmt.Errorf("%w: notes nonce required when notes ciphertext present", ErrInvalidInput)
	}
	return nil
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

// CreateAttachment records an attachment row. The handler has already written
// the encrypted blob to the blob store; it passes the resulting path here.
func (s *Service) CreateAttachment(ctx context.Context, userID, itemID, blobPath string,
	sizeBytes int64, fkCipher, fkNonce, nameCipher, nameNonce string,
) (Attachment, error) {
	if blobPath == "" || fkCipher == "" || fkNonce == "" || nameCipher == "" || nameNonce == "" {
		return Attachment{}, fmt.Errorf("%w: attachment ciphertext required", ErrInvalidInput)
	}
	if sizeBytes < 0 {
		return Attachment{}, fmt.Errorf("%w: negative size", ErrInvalidInput)
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
