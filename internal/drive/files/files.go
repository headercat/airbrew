// Package files holds the drive domain model, persistence and service: a user's
// files and folders (one self-referential tree per user) plus the upload,
// download, move, copy, star, trash and share operations on them. File bytes
// are stored in the blob store (namespace "drive"); drive_nodes rows carry the
// metadata and a sha256 content hash.
package files

import (
	"errors"
	"io"
	"time"
)

// Sentinel errors.
var (
	// ErrNotFound is returned when no node matches the lookup.
	ErrNotFound = errors.New("drive: node not found")
	// ErrFolderNotFound is returned when a referenced folder does not exist.
	ErrFolderNotFound = errors.New("drive: folder not found")
	// ErrShareNotFound is returned when no share matches the lookup.
	ErrShareNotFound = errors.New("drive: share not found")
	// ErrShareTokenTaken is returned when a generated share token collides;
	// the caller should retry with a fresh token.
	ErrShareTokenTaken = errors.New("drive: share token taken")
	// ErrInvalidInput is returned on shape-validation failure.
	ErrInvalidInput = errors.New("drive: invalid input")
	// ErrNameRequired is returned when a name is missing.
	ErrNameRequired = errors.New("drive: name required")
	// ErrCircularMove is returned when a move would create a cycle.
	ErrCircularMove = errors.New("drive: circular move")
	// ErrQuotaExceeded is returned when the user's storage quota is exceeded.
	ErrQuotaExceeded = errors.New("drive: quota exceeded")
	// ErrTooLarge is returned when an upload exceeds the per-upload limit.
	ErrTooLarge = errors.New("drive: file too large")
	// ErrPasswordRequired is returned when a password-protected share is opened
	// without one.
	ErrPasswordRequired = errors.New("drive: share password required")
	// ErrExpired is returned when a share has expired.
	ErrExpired = errors.New("drive: share expired")
)

// Kind is whether a node is a file or a folder.
type Kind string

const (
	KindFile   Kind = "file"
	KindFolder Kind = "folder"
)

// Node is one file or folder owned by a user.
type Node struct {
	ID          string
	UserID      string
	ParentID    string // "" = root
	Kind        Kind
	Name        string
	BlobPath    string // "" for folders
	ContentType string
	SizeBytes   int64
	SHA256      string
	IsStarred   bool
	DeletedAt   *time.Time
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// IsFolder reports whether the node is a folder.
func (n *Node) IsFolder() bool { return n.Kind == KindFolder }

// InTrash reports whether the node is soft-deleted.
func (n *Node) InTrash() bool { return n.DeletedAt != nil }

// Share is a public share link to a node.
type Share struct {
	ID          string
	NodeID      string
	UserID      string
	Token       string
	HasPassword bool
	ExpiresAt   *time.Time
	Downloads   int64
	IsActive    bool
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// CreateFolderInput carries the editable fields for creating a folder.
type CreateFolderInput struct {
	UserID   string
	ParentID string
	Name     string
}

// UploadInput carries the fields for uploading a file.
type UploadInput struct {
	UserID      string
	ParentID    string
	Name        string
	ContentType string
	Content     io.Reader
}

// CreateShareInput carries the editable fields for creating a share link.
type CreateShareInput struct {
	UserID    string
	NodeID    string
	Password  string // optional; "" = no password
	ExpiresAt *time.Time
}

// ListFilter controls which nodes are returned.
type ListFilter struct {
	UserID   string
	ParentID string // "" matches root; "*" matches any parent
	Folder   string // "starred", "trash", "files", "folders", "search"
	Search   string
	Kind     Kind   // "" = all
	SortBy   string // "name", "size", "created", "updated"
	SortDesc bool
	Limit    int
	Offset   int
}
