package vault

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/headercat/airbrew/internal/db"
)

// testRepo opens a migrated SQLite DB in a temp dir and returns a Repository
// bound to it.
func testRepo(t *testing.T) *Repository {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "vault-test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	if err := d.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	repo := NewRepository(d.DB)
	// Most vault tables have FK -> users(id), so seed a user per test DB.
	if _, err := d.DB.Exec(`INSERT INTO users (id, public_subject, email) VALUES ('u1', 'sub1', 'u1@example.com')`); err != nil {
		t.Fatal(err)
	}
	return repo
}

func env(userID string) KeyEnvelope {
	return KeyEnvelope{
		UserID: userID, KDFAlgorithm: "argon2id", KDFSalt: "s",
		KDFMemoryKiB: 1024, KDFIterations: 1, KDFParallelism: 1,
		ProtectedVaultKey: "k", ProtectedVaultNonce: "n",
	}
}

func TestSetupEnvelopeIsIdempotentSafe(t *testing.T) {
	repo := testRepo(t)
	ctx := context.Background()
	if err := repo.SetupEnvelope(ctx, env("u1")); err != nil {
		t.Fatalf("first setup: %v", err)
	}
	if err := repo.SetupEnvelope(ctx, env("u1")); !errors.Is(err, ErrEnvelopeExists) {
		t.Fatalf("second setup = %v, want ErrEnvelopeExists", err)
	}
}

// TestRotateEnvelopeConflictGuardsConcurrentRotation ensures the optimistic-
// concurrency guard added in the hardening pass prevents a second rotation
// using a stale version from silently clobbering the first.
func TestRotateEnvelopeConflictGuardsConcurrentRotation(t *testing.T) {
	repo := testRepo(t)
	ctx := context.Background()
	if err := repo.SetupEnvelope(ctx, env("u1")); err != nil {
		t.Fatal(err)
	}
	cur, err := repo.GetEnvelope(ctx, "u1")
	if err != nil {
		t.Fatal(err)
	}
	if cur.Version != 1 {
		t.Fatalf("initial version = %d, want 1", cur.Version)
	}
	// Rotate once with the correct version.
	e2 := env("u1")
	e2.ProtectedVaultKey = "k2"
	if err := repo.RotateEnvelope(ctx, e2, cur.Version); err != nil {
		t.Fatalf("rotate: %v", err)
	}
	// A concurrent rotation using the now-stale original version must fail.
	e3 := env("u1")
	e3.ProtectedVaultKey = "k3"
	err = repo.RotateEnvelope(ctx, e3, cur.Version)
	var ce *ConflictError
	if !errors.As(err, &ce) {
		t.Fatalf("rotate with stale version = %v, want *ConflictError", err)
	}
	got, _ := repo.GetEnvelope(ctx, "u1")
	if got.ProtectedVaultKey != "k2" {
		t.Fatalf("protected key = %q, want k2 (first rotation should win)", got.ProtectedVaultKey)
	}
	if got.Version != 2 {
		t.Fatalf("version = %d, want 2", got.Version)
	}
}

func TestItemCRUDAndRevisionGuard(t *testing.T) {
	repo := testRepo(t)
	ctx := context.Background()
	in := ItemInput{
		Type: ItemLogin, NameCipher: "n", NameNonce: "nn",
		DataCipher: "d", DataNonce: "dn",
	}
	it, err := repo.CreateItem(ctx, "u1", in)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if it.Revision == 0 {
		t.Fatal("expected non-zero revision")
	}
	got, err := repo.GetItem(ctx, "u1", it.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.ID != it.ID {
		t.Fatalf("get returned %s, want %s", got.ID, it.ID)
	}
	// Update with the wrong revision must conflict.
	_, err = repo.UpdateItem(ctx, "u1", it.ID, in, it.Revision+1)
	if !errors.As(err, new(*ConflictError)) && !isConflictErr(err) {
		t.Fatalf("update with wrong revision = %v, want conflict", err)
	}
	// Update with the correct revision archives history + bumps revision.
	upd, err := repo.UpdateItem(ctx, "u1", it.ID, ItemInput{
		Type: ItemLogin, NameCipher: "n2", NameNonce: "nn2",
		DataCipher: "d2", DataNonce: "dn2",
	}, it.Revision)
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if upd.Revision <= it.Revision {
		t.Fatalf("revision did not advance: %d -> %d", it.Revision, upd.Revision)
	}
	revs, err := repo.ListItemRevisions(ctx, "u1", it.ID)
	if err != nil {
		t.Fatalf("list revisions: %v", err)
	}
	if len(revs) != 1 {
		t.Fatalf("expected 1 archived revision, got %d", len(revs))
	}
}

// isConflictErr is a small helper because errors.As needs a pointer-to-pointer.
func isConflictErr(err error) bool {
	var ce *ConflictError
	return errors.As(err, &ce)
}

func TestSyncReturnsDeltaAndTombstones(t *testing.T) {
	repo := testRepo(t)
	ctx := context.Background()
	it, _ := repo.CreateItem(ctx, "u1", ItemInput{
		Type: ItemLogin, NameCipher: "n", NameNonce: "nn",
		DataCipher: "d", DataNonce: "dn",
	})
	first, err := repo.Sync(ctx, "u1", 0, 0)
	if err != nil {
		t.Fatalf("sync 1: %v", err)
	}
	if len(first.Items) != 1 {
		t.Fatalf("full sync items = %d, want 1", len(first.Items))
	}
	// Delta sync from the cursor should be empty until the next change.
	delta, err := repo.Sync(ctx, "u1", first.Cursor, 0)
	if err != nil {
		t.Fatalf("sync 2: %v", err)
	}
	if len(delta.Items) != 0 {
		t.Fatalf("delta items = %d, want 0", len(delta.Items))
	}
	// Soft-delete and sync again — the tombstone must show up with a higher rev.
	if _, err := repo.SoftDeleteItem(ctx, "u1", it.ID, it.Revision); err != nil {
		t.Fatalf("soft delete: %v", err)
	}
	after, err := repo.Sync(ctx, "u1", delta.Cursor, 0)
	if err != nil {
		t.Fatalf("sync 3: %v", err)
	}
	if len(after.Items) != 1 {
		t.Fatalf("expected tombstone in delta, got %d items", len(after.Items))
	}
	if after.Items[0].DeletedAt == nil {
		t.Fatalf("expected tombstoned row, got active item")
	}
}

func TestSyncPagination(t *testing.T) {
	repo := testRepo(t)
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		repo.CreateItem(ctx, "u1", ItemInput{
			Type: ItemLogin, NameCipher: "n", NameNonce: "nn",
			DataCipher: "d", DataNonce: "dn",
		})
	}
	// Page size 2 should yield HasMore until drained.
	since := int64(0)
	var total int
	for {
		res, err := repo.Sync(ctx, "u1", since, 2)
		if err != nil {
			t.Fatalf("sync: %v", err)
		}
		total += len(res.Items)
		since = res.Cursor
		if !res.HasMore {
			break
		}
	}
	if total != 5 {
		t.Fatalf("paginated total = %d, want 5", total)
	}
}

// TestSoftDeleteItemDropsAttachmentRows verifies the A3 fix: deleting an item
// also removes its attachment rows in the same tx and reports the blob paths.
func TestSoftDeleteItemDropsAttachmentRows(t *testing.T) {
	repo := testRepo(t)
	ctx := context.Background()
	it, _ := repo.CreateItem(ctx, "u1", ItemInput{
		Type: ItemLogin, NameCipher: "n", NameNonce: "nn",
		DataCipher: "d", DataNonce: "dn",
	})
	if _, err := repo.CreateAttachment(ctx, "u1", it.ID, "vault-attachments/blob1",
		10, "fk", "fkn", "nm", "nmn"); err != nil {
		t.Fatalf("create attachment: %v", err)
	}
	paths, err := repo.SoftDeleteItem(ctx, "u1", it.ID, it.Revision)
	if err != nil {
		t.Fatalf("soft delete: %v", err)
	}
	if len(paths) != 1 || paths[0] != "vault-attachments/blob1" {
		t.Fatalf("returned blob paths = %v, want [vault-attachments/blob1]", paths)
	}
	// Attachments must be gone now.
	atts, err := repo.ListAttachments(ctx, "u1", it.ID)
	if err != nil {
		t.Fatalf("list attachments: %v", err)
	}
	if len(atts) != 0 {
		t.Fatalf("attachments after item delete = %d, want 0", len(atts))
	}
}

func TestImportBundleReinsertsWithFreshIDs(t *testing.T) {
	repo := testRepo(t)
	ctx := context.Background()
	before, err := repo.Sync(ctx, "u1", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	fc, ic, err := repo.ImportBundle(ctx, "u1",
		[]Folder{{NameCipher: "fn", NameNonce: "fnn"}},
		[]Item{{
			Type: ItemLogin, NameCipher: "in", NameNonce: "inn",
			DataCipher: "id", DataNonce: "idn",
		}})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if fc != 1 || ic != 1 {
		t.Fatalf("counts = folders %d items %d, want 1/1", fc, ic)
	}
	after, err := repo.Sync(ctx, "u1", before.Cursor, 0)
	if err != nil {
		t.Fatalf("sync after import: %v", err)
	}
	if len(after.Folders) != 1 || len(after.Items) != 1 {
		t.Fatalf("after import = folders %d items %d, want 1/1", len(after.Folders), len(after.Items))
	}
}

func TestRestoreItemRevision(t *testing.T) {
	repo := testRepo(t)
	ctx := context.Background()
	it, _ := repo.CreateItem(ctx, "u1", ItemInput{
		Type: ItemLogin, NameCipher: "v1", NameNonce: "nn",
		DataCipher: "d1", DataNonce: "dn",
	})
	upd, _ := repo.UpdateItem(ctx, "u1", it.ID, ItemInput{
		Type: ItemLogin, NameCipher: "v2", NameNonce: "nn",
		DataCipher: "d2", DataNonce: "dn",
	}, it.Revision)
	revs, _ := repo.ListItemRevisions(ctx, "u1", it.ID)
	if len(revs) != 1 {
		t.Fatalf("expected 1 archived revision, got %d", len(revs))
	}
	// Restore the archived v1 snapshot onto the current v2 row.
	restored, err := repo.RestoreItemRevision(ctx, "u1", it.ID, revs[0].ID, upd.Revision)
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	if restored.NameCipher != "v1" || restored.DataCipher != "d1" {
		t.Fatalf("restored = name %q data %q, want v1/d1", restored.NameCipher, restored.DataCipher)
	}
}

func TestPurgeOldTombstonesRespectsCutoff(t *testing.T) {
	repo := testRepo(t)
	ctx := context.Background()
	it, _ := repo.CreateItem(ctx, "u1", ItemInput{
		Type: ItemLogin, NameCipher: "n", NameNonce: "nn",
		DataCipher: "d", DataNonce: "dn",
	})
	if _, err := repo.SoftDeleteItem(ctx, "u1", it.ID, it.Revision); err != nil {
		t.Fatal(err)
	}
	// Cutoff far in the past: nothing deleted (the row is only moments old).
	f, i, err := repo.PurgeOldTombstones(ctx, time.Now().Add(-365*24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if f != 0 || i != 0 {
		t.Fatalf("past cutoff purged folders=%d items=%d, want 0/0", f, i)
	}
	// Cutoff in the future: the just-deleted row is older than it, so purged.
	f, i, err = repo.PurgeOldTombstones(ctx, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if i != 1 {
		t.Fatalf("future cutoff purged items=%d, want 1", i)
	}
}
