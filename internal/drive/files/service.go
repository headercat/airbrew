package files

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"log/slog"
	"strings"
	"sync/atomic"
	"time"

	"github.com/headercat/airbrew/internal/auth/password"
	"github.com/headercat/airbrew/internal/blob"
	"github.com/headercat/airbrew/internal/id"
	"github.com/headercat/airbrew/internal/logging"
)

// Namespace is the blob-store namespace used for drive file bytes.
const Namespace = "drive"

const (
	MinSharePasswordLength = 8
	MaxSharePasswordLength = 256
	MaxShareTTL            = 365 * 24 * time.Hour
	MaxActiveSharesPerNode = 25
)

// Config tunes drive behaviour. A value <= 0 means "no limit" for that field.
type Config struct {
	// MaxUploadBytes caps a single upload; <= 0 means unlimited.
	MaxUploadBytes int64 `json:"max_upload_bytes"`
	// QuotaBytes caps a user's total live storage; <= 0 means unlimited.
	QuotaBytes int64 `json:"quota_bytes"`
}

// configDefaults applied only when a stored config omits the field entirely.
const (
	DefaultMaxUpload int64 = 50 << 20
	DefaultQuota     int64 = 1 << 30
)

// ParseConfig decodes a JSON config blob. Defaults are applied only for fields
// that are absent from the JSON, so an admin can explicitly set a field to 0
// (unlimited) and have it persist.
func ParseConfig(raw string) Config {
	c := Config{MaxUploadBytes: DefaultMaxUpload, QuotaBytes: DefaultQuota}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return c
	}
	var in struct {
		MaxUploadBytes *int64 `json:"max_upload_bytes"`
		QuotaBytes     *int64 `json:"quota_bytes"`
	}
	if err := json.Unmarshal([]byte(raw), &in); err != nil {
		return c
	}
	if in.MaxUploadBytes != nil {
		c.MaxUploadBytes = *in.MaxUploadBytes
	}
	if in.QuotaBytes != nil {
		c.QuotaBytes = *in.QuotaBytes
	}
	return c
}

// MarshalConfig encodes a config to JSON for storage.
func MarshalConfig(c Config) string {
	b, _ := json.Marshal(c)
	return string(b)
}

// Service contains drive business logic.
type Service struct {
	repo  *Repository
	blobs blob.Store
	cfg   atomic.Pointer[Config]
}

// NewService returns a Service backed by repo. blobs stores file bytes.
func NewService(repo *Repository, blobs blob.Store, cfg Config) *Service {
	s := &Service{repo: repo, blobs: blobs}
	s.cfg.Store(&cfg)
	return s
}

// SetConfig swaps the active config (live, no restart needed).
func (s *Service) SetConfig(c Config) { s.cfg.Store(&c) }

// Config returns the current active config.
func (s *Service) Config() Config { return *s.cfg.Load() }

// CreateFolder creates a folder under parentID ("" = root).
func (s *Service) CreateFolder(ctx context.Context, in CreateFolderInput) (*Node, error) {
	name := cleanName(in.Name)
	if name == "" {
		return nil, ErrNameRequired
	}
	if in.ParentID != "" {
		p, err := s.repo.GetNode(ctx, in.UserID, in.ParentID)
		if err != nil {
			return nil, err
		}
		if !p.IsFolder() {
			return nil, fmt.Errorf("%w: parent is not a folder", ErrInvalidInput)
		}
	}
	n := &Node{
		ID:       nextID(),
		UserID:   in.UserID,
		ParentID: in.ParentID,
		Kind:     KindFolder,
		Name:     name,
	}
	if err := s.repo.CreateNode(ctx, n); err != nil {
		return nil, err
	}
	return n, nil
}

// Upload stores a file under parentID ("" = root). The content is hashed
// (sha256) and counted while streaming into the blob store; the per-upload and
// quota limits are enforced.
func (s *Service) Upload(ctx context.Context, in UploadInput) (*Node, error) {
	name := cleanName(in.Name)
	if name == "" {
		return nil, ErrNameRequired
	}
	if in.ParentID != "" {
		p, err := s.repo.GetNode(ctx, in.UserID, in.ParentID)
		if err != nil {
			return nil, err
		}
		if !p.IsFolder() {
			return nil, fmt.Errorf("%w: parent is not a folder", ErrInvalidInput)
		}
	}
	if in.ContentType == "" {
		in.ContentType = "application/octet-stream"
	}

	// Enforce per-upload cap while streaming, and hash + count simultaneously.
	cfg := s.Config()
	h := sha256.New()
	var size int64
	limited := &limitReader{r: in.Content, max: cfg.MaxUploadBytes}
	tee := io.TeeReader(limited, &countWriter{h: h, n: &size})

	var blobPath string
	if s.blobs != nil {
		p, err := s.blobs.Save(ctx, Namespace, in.ContentType, tee)
		if err != nil {
			if errors.Is(err, errLimitExceeded) {
				return nil, ErrTooLarge
			}
			return nil, fmt.Errorf("drive: save blob: %w", err)
		}
		blobPath = p
	}

	// Quota is enforced atomically with the insert (CreateNodeWithQuota) to
	// close the TOCTOU window between a standalone size read and the insert.

	n := &Node{
		ID:          nextID(),
		UserID:      in.UserID,
		ParentID:    in.ParentID,
		Kind:        KindFile,
		Name:        name,
		BlobPath:    blobPath,
		ContentType: in.ContentType,
		SizeBytes:   size,
		SHA256:      hex.EncodeToString(h.Sum(nil)),
	}
	if err := s.repo.CreateNodeWithQuota(ctx, n, cfg.QuotaBytes); err != nil {
		if errors.Is(err, ErrQuotaExceeded) {
			s.deleteBlob(ctx, blobPath)
			return nil, fmt.Errorf("%w: quota %d exceeded", ErrQuotaExceeded, cfg.QuotaBytes)
		}
		s.deleteBlob(ctx, blobPath)
		return nil, err
	}
	return n, nil
}

// Download opens a file's blob for reading. The caller must close the reader.
func (s *Service) Download(ctx context.Context, userID, id string) (io.ReadCloser, *Node, error) {
	n, err := s.repo.GetNode(ctx, userID, id)
	if err != nil {
		return nil, nil, err
	}
	if n.IsFolder() {
		return nil, nil, fmt.Errorf("%w: cannot download a folder", ErrInvalidInput)
	}
	if n.BlobPath == "" || s.blobs == nil {
		return nil, nil, fmt.Errorf("%w: file content missing", ErrNotFound)
	}
	body, _, err := s.blobs.Open(ctx, n.BlobPath)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: file content missing", ErrNotFound)
	}
	return body, n, nil
}

// ArchiveFolder streams a folder subtree as a zip archive. The caller must
// close the returned reader.
func (s *Service) ArchiveFolder(ctx context.Context, userID, id string) (io.ReadCloser, *Node, error) {
	n, err := s.repo.GetNode(ctx, userID, id)
	if err != nil {
		return nil, nil, err
	}
	if !n.IsFolder() {
		return nil, nil, fmt.Errorf("%w: cannot archive a file", ErrInvalidInput)
	}
	pr, pw := io.Pipe()
	logging.Go("drive.archiveFolder", func() {
		defer func() {
			if r := recover(); r != nil {
				_ = pw.CloseWithError(fmt.Errorf("archive panicked: %v", r))
				slog.Error("drive: archive goroutine panicked", "error", r)
			}
		}()
		zw := zip.NewWriter(pw)
		err := s.writeZipFolder(ctx, zw, n, "")
		if closeErr := zw.Close(); err == nil {
			err = closeErr
		}
		_ = pw.CloseWithError(err)
	})
	return pr, n, nil
}

// Get returns one live node.
func (s *Service) Get(ctx context.Context, userID, id string) (*Node, error) {
	return s.repo.GetNode(ctx, userID, id)
}

// GetPath returns the ancestor chain root→node for breadcrumb rendering.
func (s *Service) GetPath(ctx context.Context, userID, id string) ([]*Node, error) {
	return s.repo.GetPath(ctx, userID, id)
}

// List returns nodes matching the filter.
func (s *Service) List(ctx context.Context, f ListFilter) ([]*Node, error) {
	return s.repo.ListNodes(ctx, f)
}

// ListTotal returns the total number of nodes matching the filter (ignoring
// limit/offset), for pagination.
func (s *Service) ListTotal(ctx context.Context, f ListFilter) (int, error) {
	return s.repo.CountNodesFiltered(ctx, f)
}

// Count returns the number of direct children of parentID.
func (s *Service) Count(ctx context.Context, userID, parentID string) (int, error) {
	return s.repo.CountNodes(ctx, userID, parentID)
}

// Usage returns the user's current live storage usage in bytes.
func (s *Service) Usage(ctx context.Context, userID string) (used, quota int64, err error) {
	used, err = s.repo.TotalSize(ctx, userID)
	if err != nil {
		return 0, 0, err
	}
	return used, s.Config().QuotaBytes, nil
}

// Rename changes a node's name.
func (s *Service) Rename(ctx context.Context, userID, id, name string) (*Node, error) {
	name = cleanName(name)
	if name == "" {
		return nil, ErrNameRequired
	}
	if err := s.repo.UpdateNode(ctx, userID, id, UpdateNodeFields{Name: &name}); err != nil {
		return nil, err
	}
	return s.repo.GetNode(ctx, userID, id)
}

// Move relocates a node under newParentID ("" = root). It rejects moving a
// node into itself or one of its own descendants.
func (s *Service) Move(ctx context.Context, userID, id, newParentID string) (*Node, error) {
	n, err := s.repo.GetNode(ctx, userID, id)
	if err != nil {
		return nil, err
	}
	if newParentID != "" {
		p, err := s.repo.GetNode(ctx, userID, newParentID)
		if err != nil {
			return nil, err
		}
		if !p.IsFolder() {
			return nil, fmt.Errorf("%w: target is not a folder", ErrInvalidInput)
		}
	}
	if newParentID == n.ParentID {
		return n, nil // no-op
	}
	// Circular-move guard: the new parent must not be the node itself or a
	// descendant of it.
	if desc, err := s.repo.IsDescendant(ctx, userID, id, newParentID); err != nil {
		return nil, err
	} else if desc {
		return nil, ErrCircularMove
	}
	if err := s.repo.UpdateNode(ctx, userID, id, UpdateNodeFields{ParentID: &newParentID}); err != nil {
		return nil, err
	}
	return s.repo.GetNode(ctx, userID, id)
}

// Copy duplicates a file or folder node under newParentID. Folder copies are
// recursive and preserve the source tree's names, metadata and file bytes.
func (s *Service) Copy(ctx context.Context, userID, id, newParentID, newName string) (*Node, error) {
	src, err := s.repo.GetNode(ctx, userID, id)
	if err != nil {
		return nil, err
	}
	if newParentID != "" {
		p, err := s.repo.GetNode(ctx, userID, newParentID)
		if err != nil {
			return nil, err
		}
		if !p.IsFolder() {
			return nil, fmt.Errorf("%w: target is not a folder", ErrInvalidInput)
		}
	}
	if src.IsFolder() {
		if desc, err := s.repo.IsDescendant(ctx, userID, id, newParentID); err != nil {
			return nil, err
		} else if desc {
			return nil, ErrCircularMove
		}
	}
	if err := s.ensureCopyQuota(ctx, userID, src); err != nil {
		return nil, err
	}
	name := cleanName(newName)
	if name == "" {
		name = defaultCopyName(src, newParentID)
	}
	copied, err := s.copyTree(ctx, src, newParentID, name)
	if err != nil {
		return nil, err
	}
	return copied, nil
}

func (s *Service) ensureCopyQuota(ctx context.Context, userID string, src *Node) error {
	cfg := s.Config()
	if cfg.QuotaBytes <= 0 {
		return nil
	}
	used, err := s.repo.TotalSize(ctx, userID)
	if err != nil {
		return err
	}
	extra := src.SizeBytes
	if src.IsFolder() {
		extra, err = s.repo.SubtreeSize(ctx, userID, src.ID)
		if err != nil {
			return err
		}
	}
	if used+extra > cfg.QuotaBytes {
		return fmt.Errorf("%w: quota %d exceeded", ErrQuotaExceeded, cfg.QuotaBytes)
	}
	return nil
}

func (s *Service) copyTree(ctx context.Context, src *Node, parentID, name string) (*Node, error) {
	if src.IsFolder() {
		dup := &Node{
			ID:        nextID(),
			UserID:    src.UserID,
			ParentID:  parentID,
			Kind:      KindFolder,
			Name:      name,
			IsStarred: src.IsStarred,
		}
		if err := s.repo.CreateNode(ctx, dup); err != nil {
			return nil, err
		}
		children, err := s.repo.ListChildren(ctx, src.UserID, src.ID)
		if err != nil {
			_ = s.DeletePermanent(ctx, src.UserID, dup.ID)
			return nil, err
		}
		for _, child := range children {
			if _, err := s.copyTree(ctx, child, dup.ID, child.Name); err != nil {
				_ = s.DeletePermanent(ctx, src.UserID, dup.ID)
				return nil, err
			}
		}
		return dup, nil
	}

	var blobPath string
	if src.BlobPath != "" && s.blobs != nil {
		body, ct, err := s.blobs.Open(ctx, src.BlobPath)
		if err != nil {
			return nil, fmt.Errorf("drive: open source blob: %w", err)
		}
		p, err := s.blobs.Save(ctx, Namespace, ct, body)
		body.Close()
		if err != nil {
			return nil, fmt.Errorf("drive: copy blob: %w", err)
		}
		blobPath = p
	}
	dup := &Node{
		ID:          nextID(),
		UserID:      src.UserID,
		ParentID:    parentID,
		Kind:        KindFile,
		Name:        name,
		BlobPath:    blobPath,
		ContentType: src.ContentType,
		SizeBytes:   src.SizeBytes,
		SHA256:      src.SHA256,
		IsStarred:   src.IsStarred,
	}
	if err := s.repo.CreateNodeWithQuota(ctx, dup, s.Config().QuotaBytes); err != nil {
		s.deleteBlob(ctx, blobPath)
		return nil, err
	}
	return dup, nil
}

func defaultCopyName(src *Node, parentID string) string {
	if src.ParentID == parentID {
		return cleanName("Copy of " + src.Name)
	}
	return src.Name
}

// SetStarred toggles the starred flag.
func (s *Service) SetStarred(ctx context.Context, userID, id string, starred bool) error {
	return s.repo.PatchStar(ctx, userID, id, starred)
}

// Trash soft-deletes a node (and, for folders, its subtree).
func (s *Service) Trash(ctx context.Context, userID, id string) error {
	return s.repo.Trash(ctx, userID, id)
}

// Restore clears the soft-delete flag (and, for folders, the descendants
// trashed with it). Returns the restored node. Quota is checked first so a
// restore cannot silently push the user over their storage limit.
func (s *Service) Restore(ctx context.Context, userID, id string) (*Node, error) {
	if cfg := s.Config(); cfg.QuotaBytes > 0 {
		restored, err := s.repo.SubtreeSizeAny(ctx, userID, id)
		if err != nil {
			return nil, err
		}
		used, _, err := s.Usage(ctx, userID)
		if err != nil {
			return nil, err
		}
		if used+restored > cfg.QuotaBytes {
			return nil, fmt.Errorf("%w: restoring %d bytes would exceed quota %d", ErrQuotaExceeded, restored, cfg.QuotaBytes)
		}
	}
	if err := s.repo.Restore(ctx, userID, id); err != nil {
		return nil, err
	}
	return s.repo.GetNodeAny(ctx, userID, id)
}

// Patch applies one or more of rename/move/star in a single call. Fields left
// nil are ignored. It delegates to Rename/Move/SetStarred in sequence.
func (s *Service) Patch(ctx context.Context, userID, id string, name *string, parentID *string, starred *bool) (*Node, error) {
	if name != nil {
		if _, err := s.Rename(ctx, userID, id, *name); err != nil {
			return nil, err
		}
	}
	if parentID != nil {
		if _, err := s.Move(ctx, userID, id, *parentID); err != nil {
			return nil, err
		}
	}
	if starred != nil {
		if err := s.SetStarred(ctx, userID, id, *starred); err != nil {
			return nil, err
		}
	}
	return s.repo.GetNode(ctx, userID, id)
}

// DeletePermanent removes a node and its subtree for good, purging its blobs.
func (s *Service) DeletePermanent(ctx context.Context, userID, id string) error {
	paths, err := s.repo.DeletePermanent(ctx, userID, id)
	if err != nil {
		return err
	}
	for _, p := range paths {
		s.deleteBlob(ctx, p)
	}
	return nil
}

// EmptyTrash permanently removes every trashed node and purges their blobs.
func (s *Service) EmptyTrash(ctx context.Context, userID string) error {
	paths, err := s.repo.EmptyTrash(ctx, userID)
	if err != nil {
		return err
	}
	for _, p := range paths {
		s.deleteBlob(ctx, p)
	}
	return nil
}

// --- shares ----------------------------------------------------------------

// CreateShare creates a public share link for a file node, optionally password
// protected and/or expiring. Folders cannot be shared (share open resolves a
// single file).
func (s *Service) CreateShare(ctx context.Context, in CreateShareInput) (*Share, error) {
	n, err := s.repo.GetNode(ctx, in.UserID, in.NodeID)
	if err != nil {
		return nil, err
	}
	if n.IsFolder() {
		return nil, fmt.Errorf("%w: folders cannot be shared", ErrInvalidInput)
	}
	if in.Password != "" {
		if len([]rune(in.Password)) < MinSharePasswordLength {
			return nil, fmt.Errorf("%w: share password must be at least %d characters", ErrInvalidInput, MinSharePasswordLength)
		}
		if len(in.Password) > MaxSharePasswordLength {
			return nil, fmt.Errorf("%w: share password is too long", ErrInvalidInput)
		}
	}
	if in.ExpiresAt != nil && in.ExpiresAt.After(time.Now().UTC().Add(MaxShareTTL)) {
		return nil, fmt.Errorf("%w: share expiry is too far in the future", ErrInvalidInput)
	}
	active, err := s.repo.CountActiveSharesByNode(ctx, in.UserID, in.NodeID, time.Now().UTC())
	if err != nil {
		return nil, err
	}
	if active >= MaxActiveSharesPerNode {
		return nil, fmt.Errorf("%w: active share limit reached", ErrInvalidInput)
	}
	var pwHash string
	if in.Password != "" {
		h, err := password.Hash(in.Password)
		if err != nil {
			return nil, err
		}
		pwHash = h
	}
	// Retry on the (astronomically unlikely) token collision.
	for attempt := 0; attempt < 4; attempt++ {
		sh := &Share{
			ID:        nextID(),
			NodeID:    in.NodeID,
			UserID:    in.UserID,
			Token:     id.New(),
			IsActive:  true,
			ExpiresAt: in.ExpiresAt,
		}
		if err := s.repo.CreateShare(ctx, sh, pwHash); err != nil {
			if errors.Is(err, ErrShareTokenTaken) {
				continue
			}
			return nil, err
		}
		sh.HasPassword = in.Password != ""
		return sh, nil
	}
	return nil, fmt.Errorf("drive: could not allocate share token")
}

// ListShares returns the user's shares, joined to their source node so the
// Shared view can label each link and flag a trashed source.
func (s *Service) ListShares(ctx context.Context, userID string) ([]*Share, error) {
	return s.repo.ListSharesWithNode(ctx, userID)
}

// ListSharesByNode returns the shares for a node.
func (s *Service) ListSharesByNode(ctx context.Context, userID, nodeID string) ([]*Share, error) {
	return s.repo.ListSharesByNode(ctx, userID, nodeID)
}

// DeleteShare revokes a share.
func (s *Service) DeleteShare(ctx context.Context, userID, id string) error {
	return s.repo.DeleteShare(ctx, userID, id)
}

// OpenShare resolves a public share by token. It enforces active/expiry and
// (if set) the password, returning the node to serve and the share (so callers
// can surface expiry/has-password to a landing page).
func (s *Service) OpenShare(ctx context.Context, token, passwordAttempt string) (*Node, *Share, error) {
	sh, err := s.repo.GetShareByToken(ctx, token)
	if err != nil {
		return nil, nil, err
	}
	if !sh.IsActive {
		return nil, nil, ErrShareNotFound
	}
	if sh.ExpiresAt != nil && sh.ExpiresAt.Before(time.Now().UTC()) {
		return nil, nil, ErrExpired
	}
	if sh.HasPassword {
		if passwordAttempt == "" {
			return nil, nil, ErrPasswordRequired
		}
		hashed, err := s.repo.SharePasswordHash(ctx, sh.ID)
		if err != nil {
			return nil, nil, err
		}
		if err := password.Verify(passwordAttempt, hashed); err != nil {
			return nil, nil, ErrPasswordRequired
		}
	}
	n, err := s.repo.GetShareNode(ctx, sh)
	if err != nil {
		return nil, nil, err
	}
	return n, sh, nil
}

// IncDownload bumps a share's download counter.
func (s *Service) IncDownload(ctx context.Context, token string) {
	_ = s.repo.IncrementShareDownloads(ctx, token)
}

// --- internal --------------------------------------------------------------

func (s *Service) deleteBlob(ctx context.Context, path string) {
	if s.blobs != nil && path != "" {
		_ = s.blobs.Delete(ctx, path)
	}
}

func (s *Service) writeZipFolder(ctx context.Context, zw *zip.Writer, folder *Node, prefix string) error {
	children, err := s.repo.ListChildren(ctx, folder.UserID, folder.ID)
	if err != nil {
		return err
	}
	for _, child := range children {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		name := prefix + child.Name
		if child.IsFolder() {
			if _, err := zw.CreateHeader(&zip.FileHeader{Name: name + "/", Modified: child.UpdatedAt}); err != nil {
				return err
			}
			if err := s.writeZipFolder(ctx, zw, child, name+"/"); err != nil {
				return err
			}
			continue
		}
		if child.BlobPath == "" || s.blobs == nil {
			return fmt.Errorf("%w: file content missing", ErrNotFound)
		}
		body, _, err := s.blobs.Open(ctx, child.BlobPath)
		if err != nil {
			return fmt.Errorf("drive: open zip source blob: %w", err)
		}
		w, err := zw.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Deflate, Modified: child.UpdatedAt})
		if err != nil {
			body.Close()
			return err
		}
		if _, err := io.Copy(w, body); err != nil {
			body.Close()
			return err
		}
		if err := body.Close(); err != nil {
			return err
		}
	}
	return nil
}

// cleanName trims and rejects names containing path separators or the . / ..
// sentinels.
func cleanName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" || name == "." || name == ".." {
		return ""
	}
	if strings.ContainsAny(name, "/\\\x00") {
		return ""
	}
	// Cap length by rune (not byte) so a multi-byte name isn't split mid-rune.
	if r := []rune(name); len(r) > 255 {
		name = string(r[:255])
	}
	return name
}

// limitReader returns errLimitExceeded from Read once more than max bytes have
// been read (max <= 0 disables the limit).
type limitReader struct {
	r   io.Reader
	max int64
	n   int64
}

var errLimitExceeded = errors.New("drive: limit exceeded")

func (l *limitReader) Read(p []byte) (int, error) {
	if l.max > 0 && l.n >= l.max {
		return 0, errLimitExceeded
	}
	n, err := l.r.Read(p)
	l.n += int64(n)
	if l.max > 0 && l.n > l.max {
		// Report how far over we are; signal the caller to abort.
		return n, errLimitExceeded
	}
	return n, err
}

// countWriter feeds bytes into a hash and tracks the total size.
type countWriter struct {
	h hash.Hash
	n *int64
}

func (c *countWriter) Write(p []byte) (int, error) {
	*c.n += int64(len(p))
	return c.h.Write(p)
}
