package vault

import (
	"context"
	"encoding/base64"
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
		UserID: userID, KDFAlgorithm: "argon2id", KDFSalt: b64bytes(16, 1),
		KDFMemoryKiB: 1024, KDFIterations: 1, KDFParallelism: 1,
		ProtectedVaultKey: cipherFixture(48, 2), ProtectedVaultNonce: nonceFixture(3),
	}
}

func b64bytes(n int, seed byte) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = seed + byte(i%17)
	}
	return base64.StdEncoding.EncodeToString(b)
}

func cipherFixture(n int, seed byte) string { return b64bytes(n, seed) }

func nonceFixture(seed byte) string { return b64bytes(12, seed) }

func itemInputFixture() ItemInput {
	return ItemInput{
		Type: ItemLogin, NameCipher: cipherFixture(24, 10), NameNonce: nonceFixture(20),
		DataCipher: cipherFixture(32, 30), DataNonce: nonceFixture(40),
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
	in := itemInputFixture()
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
		Type: ItemLogin, NameCipher: cipherFixture(24, 11), NameNonce: nonceFixture(21),
		DataCipher: cipherFixture(32, 31), DataNonce: nonceFixture(41),
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
	it, _ := repo.CreateItem(ctx, "u1", itemInputFixture())
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
			Type: ItemLogin, NameCipher: cipherFixture(24, byte(10+i)), NameNonce: nonceFixture(byte(20 + i)),
			DataCipher: cipherFixture(32, byte(30+i)), DataNonce: nonceFixture(byte(40 + i)),
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
	it, _ := repo.CreateItem(ctx, "u1", itemInputFixture())
	if _, err := repo.CreateAttachment(ctx, "u1", it.ID, "vault-attachments/blob1",
		10, cipherFixture(48, 50), nonceFixture(60), cipherFixture(24, 70), nonceFixture(80)); err != nil {
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
		[]Folder{{NameCipher: cipherFixture(24, 12), NameNonce: nonceFixture(22)}},
		[]Item{{
			Type: ItemLogin, NameCipher: cipherFixture(24, 13), NameNonce: nonceFixture(23),
			DataCipher: cipherFixture(32, 33), DataNonce: nonceFixture(43),
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
		Type: ItemLogin, NameCipher: cipherFixture(24, 14), NameNonce: nonceFixture(24),
		DataCipher: cipherFixture(32, 34), DataNonce: nonceFixture(44),
	})
	upd, _ := repo.UpdateItem(ctx, "u1", it.ID, ItemInput{
		Type: ItemLogin, NameCipher: cipherFixture(24, 15), NameNonce: nonceFixture(25),
		DataCipher: cipherFixture(32, 35), DataNonce: nonceFixture(45),
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
	if restored.NameCipher != cipherFixture(24, 14) || restored.DataCipher != cipherFixture(32, 34) {
		t.Fatalf("restored = name %q data %q, want archived snapshot", restored.NameCipher, restored.DataCipher)
	}
}

func TestPurgeOldTombstonesRespectsCutoff(t *testing.T) {
	repo := testRepo(t)
	ctx := context.Background()
	it, _ := repo.CreateItem(ctx, "u1", itemInputFixture())
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
