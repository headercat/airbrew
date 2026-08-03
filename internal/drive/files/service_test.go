package files

import (
	"bytes"
	"context"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/headercat/airbrew/internal/db"
)

// memStore is an in-memory blob.Store for tests.
type memStore struct {
	files map[string][]byte
}

func newMemStore() *memStore { return &memStore{files: map[string][]byte{}} }

func (m *memStore) Save(_ context.Context, namespace, _ string, r io.Reader) (string, error) {
	id := namespace + "/" + nextID() + ".bin"
	b, err := io.ReadAll(r)
	if err != nil {
		return "", err
	}
	m.files[id] = b
	return id, nil
}

func (m *memStore) Open(_ context.Context, path string) (io.ReadCloser, string, error) {
	b, ok := m.files[path]
	if !ok {
		return nil, "", errors.New("not found")
	}
	return io.NopCloser(bytes.NewReader(b)), "application/octet-stream", nil
}

func (m *memStore) Delete(_ context.Context, path string) error {
	delete(m.files, path)
	return nil
}

func (m *memStore) List(_ context.Context, namespace string) ([]string, error) {
	var out []string
	for k := range m.files {
		if strings.HasPrefix(k, namespace+"/") {
			out = append(out, k)
		}
	}
	return out, nil
}

func testService(t *testing.T) (*Service, string) {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "drive-test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	if err := d.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	uid := "user_" + nextID()
	if _, err := d.DB.ExecContext(context.Background(),
		`INSERT INTO users (id, email, public_subject, status) VALUES (?, ?, ?, 'active')`,
		uid, uid+"@example.com", uid); err != nil {
		t.Fatal(err)
	}
	repo := NewRepository(d.DB)
	store := newMemStore()
	// Per-upload cap (5 MiB) above quota (2 MiB) so the two limits can be
	// exercised independently in tests.
	return NewService(repo, store, Config{MaxUploadBytes: 5 << 20, QuotaBytes: 2 << 20}), uid
}

func TestCreateFolderAndList(t *testing.T) {
	svc, uid := testService(t)
	ctx := context.Background()
	f, err := svc.CreateFolder(ctx, CreateFolderInput{UserID: uid, Name: "Docs"})
	if err != nil {
		t.Fatal(err)
	}
	if f.Kind != KindFolder || f.ParentID != "" {
		t.Fatalf("unexpected folder %+v", f)
	}
	// Empty name rejected.
	if _, err := svc.CreateFolder(ctx, CreateFolderInput{UserID: uid, Name: "  "}); !errors.Is(err, ErrNameRequired) {
		t.Fatalf("expected ErrNameRequired, got %v", err)
	}
	// Sub-folder.
	sub, err := svc.CreateFolder(ctx, CreateFolderInput{UserID: uid, ParentID: f.ID, Name: "Tax"})
	if err != nil {
		t.Fatal(err)
	}
	nodes, err := svc.List(ctx, ListFilter{UserID: uid, ParentID: f.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 1 || nodes[0].ID != sub.ID {
		t.Fatalf("expected sub-folder listed, got %+v", nodes)
	}
}

func TestUploadQuotaAndHash(t *testing.T) {
	svc, uid := testService(t)
	ctx := context.Background()
	content := strings.Repeat("a", 100)
	n, err := svc.Upload(ctx, UploadInput{
		UserID: uid, Name: "note.txt", ContentType: "text/plain",
		Content: strings.NewReader(content),
	})
	if err != nil {
		t.Fatal(err)
	}
	if n.SizeBytes != int64(len(content)) || n.SHA256 == "" {
		t.Fatalf("unexpected node %+v", n)
	}
	// Download round-trips.
	body, _, err := svc.Download(ctx, uid, n.ID)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(body)
	body.Close()
	if string(got) != content {
		t.Fatalf("download mismatch")
	}
	// Quota exceeded: 3 MiB > 2 MiB quota.
	_, err = svc.Upload(ctx, UploadInput{
		UserID: uid, Name: "big.bin", ContentType: "application/octet-stream",
		Content: bytes.NewReader(make([]byte, 3<<20)),
	})
	if !errors.Is(err, ErrQuotaExceeded) {
		t.Fatalf("expected ErrQuotaExceeded, got %v", err)
	}
	// Per-upload cap: a single 6 MiB upload > 5 MiB cap fires while streaming.
	_, err = svc.Upload(ctx, UploadInput{
		UserID: uid, Name: "cap.bin", Content: bytes.NewReader(make([]byte, 6<<20)),
	})
	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("expected ErrTooLarge, got %v", err)
	}
}

func TestConcurrentUploadsCannotOverrunQuota(t *testing.T) {
	svc, uid := testService(t)
	svc.repo.db.SetMaxOpenConns(8)
	svc.SetConfig(Config{MaxUploadBytes: 1 << 20, QuotaBytes: 100})
	ctx := context.Background()

	start := make(chan struct{})
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := svc.Upload(ctx, UploadInput{
				UserID:  uid,
				Name:    "race.bin",
				Content: bytes.NewReader(make([]byte, 80)),
			})
			errs <- err
		}()
	}
	close(start)
	wg.Wait()
	close(errs)

	var successes, quotaErrors int
	for err := range errs {
		if err == nil {
			successes++
			continue
		}
		if errors.Is(err, ErrQuotaExceeded) {
			quotaErrors++
			continue
		}
		t.Fatalf("unexpected upload error: %v", err)
	}
	if successes != 1 || quotaErrors != 1 {
		t.Fatalf("expected one success and one quota error, got %d successes and %d quota errors", successes, quotaErrors)
	}
	used, _, err := svc.Usage(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	if used > 100 {
		t.Fatalf("quota overrun: used=%d", used)
	}
}

func TestMoveCircularGuard(t *testing.T) {
	svc, uid := testService(t)
	ctx := context.Background()
	a, _ := svc.CreateFolder(ctx, CreateFolderInput{UserID: uid, Name: "A"})
	b, _ := svc.CreateFolder(ctx, CreateFolderInput{UserID: uid, ParentID: a.ID, Name: "B"})
	// Move A into B (its own descendant) must fail.
	if _, err := svc.Move(ctx, uid, a.ID, b.ID); !errors.Is(err, ErrCircularMove) {
		t.Fatalf("expected ErrCircularMove, got %v", err)
	}
	// Move B into root succeeds.
	moved, err := svc.Move(ctx, uid, b.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if moved.ParentID != "" {
		t.Fatalf("expected root, got parent %q", moved.ParentID)
	}
}

func TestCopyFileAndFolderTree(t *testing.T) {
	svc, uid := testService(t)
	ctx := context.Background()

	root, err := svc.CreateFolder(ctx, CreateFolderInput{UserID: uid, Name: "Project"})
	if err != nil {
		t.Fatal(err)
	}
	sub, err := svc.CreateFolder(ctx, CreateFolderInput{UserID: uid, ParentID: root.ID, Name: "Assets"})
	if err != nil {
		t.Fatal(err)
	}
	file, err := svc.Upload(ctx, UploadInput{
		UserID: uid, ParentID: sub.ID, Name: "brief.txt", Content: strings.NewReader("hello"),
	})
	if err != nil {
		t.Fatal(err)
	}

	fileCopy, err := svc.Copy(ctx, uid, file.ID, sub.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if fileCopy.Name != "Copy of brief.txt" || fileCopy.ParentID != sub.ID || fileCopy.BlobPath == file.BlobPath {
		t.Fatalf("unexpected file copy %+v", fileCopy)
	}
	body, _, err := svc.Download(ctx, uid, fileCopy.ID)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(body)
	body.Close()
	if string(got) != "hello" {
		t.Fatalf("copied file content mismatch: %q", got)
	}

	treeCopy, err := svc.Copy(ctx, uid, root.ID, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if treeCopy.Name != "Copy of Project" || treeCopy.Kind != KindFolder {
		t.Fatalf("unexpected tree copy %+v", treeCopy)
	}
	children, err := svc.repo.ListChildren(ctx, uid, treeCopy.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(children) != 1 || children[0].Name != "Assets" {
		t.Fatalf("expected copied Assets folder, got %+v", children)
	}
	grandchildren, err := svc.repo.ListChildren(ctx, uid, children[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, child := range grandchildren {
		names[child.Name] = true
	}
	if len(grandchildren) != 2 || !names["brief.txt"] || !names["Copy of brief.txt"] {
		t.Fatalf("expected copied files, got %+v", grandchildren)
	}

	if _, err := svc.Copy(ctx, uid, root.ID, sub.ID, "Bad copy"); !errors.Is(err, ErrCircularMove) {
		t.Fatalf("expected ErrCircularMove, got %v", err)
	}
}

func TestTrashRestoreDelete(t *testing.T) {
	svc, uid := testService(t)
	ctx := context.Background()
	root, _ := svc.CreateFolder(ctx, CreateFolderInput{UserID: uid, Name: "Root"})
	sub, _ := svc.CreateFolder(ctx, CreateFolderInput{UserID: uid, ParentID: root.ID, Name: "Sub"})
	file, _ := svc.Upload(ctx, UploadInput{
		UserID: uid, ParentID: sub.ID, Name: "f.txt", Content: strings.NewReader("x"),
	})
	// Trash the root folder → subtree trashed.
	if err := svc.Trash(ctx, uid, root.ID); err != nil {
		t.Fatal(err)
	}
	trash, _ := svc.List(ctx, ListFilter{UserID: uid, Folder: "trash"})
	if len(trash) != 1 || trash[0].ID != root.ID {
		t.Fatalf("expected only root in trash, got %+v", trash)
	}
	// Root listing empty.
	live, _ := svc.List(ctx, ListFilter{UserID: uid})
	if len(live) != 0 {
		t.Fatalf("expected empty root, got %+v", live)
	}
	// Restore the subtree.
	if _, err := svc.Restore(ctx, uid, root.ID); err != nil {
		t.Fatal(err)
	}
	live, _ = svc.List(ctx, ListFilter{UserID: uid})
	if len(live) != 1 || live[0].ID != root.ID {
		t.Fatalf("expected root restored, got %+v", live)
	}
	// Permanent delete purges the subtree.
	if err := svc.DeletePermanent(ctx, uid, root.ID); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{root.ID, sub.ID, file.ID} {
		if _, err := svc.Get(ctx, uid, id); !errors.Is(err, ErrNotFound) {
			t.Fatalf("expected %s gone, got %v", id, err)
		}
	}
}

func TestSharePasswordAndExpiry(t *testing.T) {
	svc, uid := testService(t)
	ctx := context.Background()
	file, _ := svc.Upload(ctx, UploadInput{UserID: uid, Name: "s.txt", Content: strings.NewReader("secret")})

	// No-password share opens immediately.
	sh, err := svc.CreateShare(ctx, CreateShareInput{UserID: uid, NodeID: file.ID})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.OpenShare(ctx, sh.Token, ""); err != nil {
		t.Fatalf("open no-pw share: %v", err)
	}

	// Password share requires the password.
	pw, err := svc.CreateShare(ctx, CreateShareInput{UserID: uid, NodeID: file.ID, Password: "hunter22"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.OpenShare(ctx, pw.Token, ""); !errors.Is(err, ErrPasswordRequired) {
		t.Fatalf("expected ErrPasswordRequired, got %v", err)
	}
	if _, _, err := svc.OpenShare(ctx, pw.Token, "wrong"); !errors.Is(err, ErrPasswordRequired) {
		t.Fatalf("expected ErrPasswordRequired on wrong pw, got %v", err)
	}
	if _, _, err := svc.OpenShare(ctx, pw.Token, "hunter22"); err != nil {
		t.Fatalf("correct pw should open: %v", err)
	}

	// Expired share is rejected.
	past := time.Now().UTC().Add(-time.Hour)
	exp, err := svc.CreateShare(ctx, CreateShareInput{UserID: uid, NodeID: file.ID, ExpiresAt: &past})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.OpenShare(ctx, exp.Token, ""); !errors.Is(err, ErrExpired) {
		t.Fatalf("expected ErrExpired, got %v", err)
	}

	// Folder cannot be shared.
	folder, _ := svc.CreateFolder(ctx, CreateFolderInput{UserID: uid, Name: "f"})
	if _, err := svc.CreateShare(ctx, CreateShareInput{UserID: uid, NodeID: folder.ID}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("expected ErrInvalidInput sharing a folder, got %v", err)
	}
}

func TestShareCreationLimits(t *testing.T) {
	svc, uid := testService(t)
	ctx := context.Background()
	file, err := svc.Upload(ctx, UploadInput{UserID: uid, Name: "limits.txt", Content: strings.NewReader("data")})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := svc.CreateShare(ctx, CreateShareInput{UserID: uid, NodeID: file.ID, Password: "short"}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("expected ErrInvalidInput for short password, got %v", err)
	}
	farFuture := time.Now().UTC().Add(MaxShareTTL + time.Hour)
	if _, err := svc.CreateShare(ctx, CreateShareInput{UserID: uid, NodeID: file.ID, ExpiresAt: &farFuture}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("expected ErrInvalidInput for far future expiry, got %v", err)
	}

	for i := 0; i < MaxActiveSharesPerNode; i++ {
		if _, err := svc.CreateShare(ctx, CreateShareInput{UserID: uid, NodeID: file.ID}); err != nil {
			t.Fatalf("share %d: %v", i, err)
		}
	}
	if _, err := svc.CreateShare(ctx, CreateShareInput{UserID: uid, NodeID: file.ID}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("expected ErrInvalidInput for share limit, got %v", err)
	}
}

func TestJanitorSweepsOrphans(t *testing.T) {
	svc, uid := testService(t)
	ctx := context.Background()
	file, _ := svc.Upload(ctx, UploadInput{UserID: uid, Name: "o.txt", Content: strings.NewReader("data")})
	repo := svc.repo
	store := svc.blobs.(*memStore)
	// Inject an orphan blob that no node references.
	orphan, _ := store.Save(ctx, Namespace, "application/octet-stream", strings.NewReader("orphan"))
	known, _ := repo.AllBlobPathsAll(ctx)
	if !contains(known, file.BlobPath) {
		t.Fatal("expected file blob path known")
	}
	jan := NewJanitor(repo, store, nil)
	jan.runOnce(ctx)
	if _, ok := store.files[orphan]; ok {
		t.Fatal("expected orphan swept")
	}
	if _, ok := store.files[file.BlobPath]; !ok {
		t.Fatal("expected live file blob preserved")
	}
}

func TestAllBlobPathsIsUserScoped(t *testing.T) {
	svc, uid := testService(t)
	ctx := context.Background()
	own, err := svc.Upload(ctx, UploadInput{UserID: uid, Name: "own.txt", Content: strings.NewReader("own")})
	if err != nil {
		t.Fatal(err)
	}
	otherUID := "user_" + nextID()
	if _, err := svc.repo.db.ExecContext(ctx,
		`INSERT INTO users (id, email, public_subject, status) VALUES (?, ?, ?, 'active')`,
		otherUID, otherUID+"@example.com", otherUID); err != nil {
		t.Fatal(err)
	}
	other, err := svc.Upload(ctx, UploadInput{UserID: otherUID, Name: "other.txt", Content: strings.NewReader("other")})
	if err != nil {
		t.Fatal(err)
	}
	paths, err := svc.repo.AllBlobPaths(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	if !contains(paths, own.BlobPath) {
		t.Fatal("expected own blob path")
	}
	if contains(paths, other.BlobPath) {
		t.Fatal("did not expect another user's blob path")
	}
}

func contains(haystack []string, needle string) bool {
	for _, h := range haystack {
		if h == needle {
			return true
		}
	}
	return false
}
