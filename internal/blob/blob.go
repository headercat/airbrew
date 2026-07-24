// Package blob provides simple filesystem-backed binary storage for uploaded
// user content (avatars, future attachments).
//
// The local implementation writes files under a configured root directory
// using random nanoid-style filenames. Files are addressed by the leading
// path segment returned from Save.
package blob

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/headercat/airbrew/internal/id"
)

// Store is the interface used by handlers to persist and read binary blobs.
type Store interface {
	// Save writes the blob to storage and returns a path that can be appended
	// to the public download URL prefix (e.g. "avatars/abc123.png").
	Save(ctx context.Context, namespace, contentType string, r io.Reader) (path string, err error)

	// Open returns a reader for the blob and its content type.
	Open(ctx context.Context, path string) (io.ReadCloser, string, error)
}

// LocalStore writes files under a single root directory.
type LocalStore struct {
	root string
}

// NewLocal creates a LocalStore rooted at the given directory, creating it if
// necessary.
func NewLocal(root string) (*LocalStore, error) {
	if root == "" {
		return nil, errors.New("blob: root is required")
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, fmt.Errorf("blob: mkdir %q: %w", root, err)
	}
	return &LocalStore{root: root}, nil
}

// Root returns the on-disk root directory (used by HTTP file servers).
func (s *LocalStore) Root() string { return s.root }

// Save writes the blob to <root>/<namespace>/<id>.<ext> and returns the
// relative path. The extension is inferred from contentType via ExtFor().
func (s *LocalStore) Save(ctx context.Context, namespace, contentType string, r io.Reader) (string, error) {
	namespace = sanitizeSegment(namespace)
	if namespace == "" {
		return "", errors.New("blob: namespace required")
	}
	dir := filepath.Join(s.root, namespace)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("blob: mkdir %q: %w", dir, err)
	}
	name := id.New() + ExtFor(contentType)
	full := filepath.Join(dir, name)
	f, err := os.Create(full)
	if err != nil {
		return "", fmt.Errorf("blob: create %q: %w", full, err)
	}
	defer f.Close()
	if _, err := io.Copy(f, r); err != nil {
		_ = os.Remove(full)
		return "", fmt.Errorf("blob: write: %w", err)
	}
	return namespace + "/" + name, nil
}

// Open returns a reader and content type for the given relative path.
func (s *LocalStore) Open(ctx context.Context, path string) (io.ReadCloser, string, error) {
	clean, err := safeJoin(s.root, path)
	if err != nil {
		return nil, "", err
	}
	f, err := os.Open(clean)
	if err != nil {
		return nil, "", err
	}
	return f, ContentTypeFor(filepath.Ext(path)), nil
}

// safeJoin prevents path traversal: requires the cleaned joined path to remain
// inside root.
func safeJoin(root, p string) (string, error) {
	cleaned := filepath.Clean("/" + strings.TrimPrefix(p, "/"))
	joined := filepath.Join(root, cleaned)
	rel, err := filepath.Rel(root, joined)
	if err != nil || strings.HasPrefix(rel, "..") {
		return "", fmt.Errorf("blob: path escapes root: %q", p)
	}
	return joined, nil
}

// sanitizeSegment keeps a path segment suitable as a directory name:
// letters, digits, dot, dash and underscore only; trimmed and lowercased.
func sanitizeSegment(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z',
			r >= '0' && r <= '9',
			r == '.', r == '-', r == '_':
			b.WriteRune(r)
		}
	}
	return b.String()
}

// ExtFor maps an image content type to its file extension, defaulting to .bin.
func ExtFor(contentType string) string {
	switch strings.ToLower(strings.TrimSpace(contentType)) {
	case "image/png":
		return ".png"
	case "image/jpeg", "image/jpg":
		return ".jpg"
	case "image/webp":
		return ".webp"
	case "image/gif":
		return ".gif"
	case "image/svg+xml":
		return ".svg"
	case "application/pdf":
		return ".pdf"
	case "text/plain":
		return ".txt"
	default:
		return ".bin"
	}
}

// ContentTypeFor is the inverse of ExtFor.
func ContentTypeFor(ext string) string {
	switch strings.ToLower(ext) {
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".webp":
		return "image/webp"
	case ".gif":
		return "image/gif"
	case ".svg":
		return "image/svg+xml"
	case ".pdf":
		return "application/pdf"
	default:
		return "application/octet-stream"
	}
}
