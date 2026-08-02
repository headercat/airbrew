// Package vault holds the domain model, persistence and service for the
// zero-knowledge password vault.
//
// The server only ever sees ciphertext: item/folder fields arrive as base64
// AES-256-GCM blobs plus their nonces. A per-user monotonic revision counter
// (vault_sync_state) is stamped on every changed row to drive multi-device
// delta sync. See docs/passwords.md for the cryptographic design.
package vault

import (
	"errors"
	"time"
)

// Sentinel errors returned by the repository and service.
var (
	// ErrNotFound is returned when no row matches the lookup.
	ErrNotFound = errors.New("vault: not found")
	// ErrEnvelopeExists is returned by Setup when a key envelope already exists.
	ErrEnvelopeExists = errors.New("vault: envelope already set up")
	// ErrInvalidInput is returned when ciphertext/nonces are missing.
	ErrInvalidInput = errors.New("vault: invalid input")
)

// ConflictError signals an optimistic-concurrency mismatch. The server's
// current row is attached so the client can merge.
type ConflictError struct {
	CurrentRow any // *Item or *Folder
}

func (e *ConflictError) Error() string { return "vault: revision conflict" }

// KeyEnvelope is the one-per-user encrypted vault-key wrapper. The client
// derives the master key from the master password using the KDF parameters and
// salt stored here, then decrypts ProtectedVaultKey to obtain the vault key.
//
// Version is a monotonic counter bumped on every rewrite (setup/rotate). It is
// the optimistic-concurrency guard for rotation: a master-password change must
// carry the version the client last saw, otherwise the server reports a
// ConflictError so two concurrent rotations cannot silently clobber one
// another and lock a device out.
type KeyEnvelope struct {
	UserID              string
	KDFAlgorithm        string
	KDFSalt             string
	KDFMemoryKiB        int
	KDFIterations       int
	KDFParallelism      int
	ProtectedVaultKey   string
	ProtectedVaultNonce string
	Version             int64
	CreatedAt           time.Time
	UpdatedAt           time.Time
}

// ItemType enumerates the supported vault entry kinds.
type ItemType string

const (
	ItemLogin      ItemType = "login"
	ItemSecureNote ItemType = "secure_note"
	ItemCard       ItemType = "card"
	ItemIdentity   ItemType = "identity"
)

// Valid reports whether t is a supported item type.
func (t ItemType) Valid() bool {
	switch t {
	case ItemLogin, ItemSecureNote, ItemCard, ItemIdentity:
		return true
	}
	return false
}

// Folder is an encrypted folder (only its name is encrypted).
type Folder struct {
	ID         string
	UserID     string
	NameCipher string
	NameNonce  string
	Revision   int64
	CreatedAt  time.Time
	UpdatedAt  time.Time
	DeletedAt  *time.Time
}

// Item is a single encrypted vault entry. DataCipher holds the type-specific
// JSON (username/password/uris/totp for logins, card fields, identity fields,
// custom fields) so the server treats all item kinds uniformly.
type Item struct {
	ID          string
	UserID      string
	Type        ItemType
	FolderID    string
	NameCipher  string
	NameNonce   string
	DataCipher  string
	DataNonce   string
	NotesCipher string
	NotesNonce  string
	Favorite    bool
	Reprompt    bool
	Revision    int64
	CreatedAt   time.Time
	UpdatedAt   time.Time
	DeletedAt   *time.Time
}

// ItemInput carries the encrypted, user-controlled fields of an item. It is
// used for both create and update.
type ItemInput struct {
	Type        ItemType
	FolderID    string
	NameCipher  string
	NameNonce   string
	DataCipher  string
	DataNonce   string
	NotesCipher string
	NotesNonce  string
	Favorite    bool
	Reprompt    bool
}

// Attachment is an encrypted file attached to an item. The file contents are
// stored as an opaque blob (see internal/blob); here we keep the encrypted
// per-file key (wrapped by the vault key), the encrypted filename, and the
// original plaintext size for display. The blob itself is nonce||ciphertext
// so the client can split and decrypt without an extra stored nonce.
type Attachment struct {
	ID            string
	ItemID        string
	BlobPath      string
	SizeBytes     int64
	FileKeyCipher string
	FileKeyNonce  string
	NameCipher    string
	NameNonce     string
	CreatedAt     time.Time
}
