package chat

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/headercat/airbrew/internal/db"
)

func testRepo(t *testing.T) (*Repository, []string) {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "chat-test.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	if err := d.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	users := []string{"u_alice", "u_bob", "u_caro"}
	for _, uid := range users {
		if _, err := d.DB.ExecContext(context.Background(),
			`INSERT INTO users (id, email, public_subject, status, display_name)
			 VALUES (?, ?, ?, 'active', ?)`,
			uid, uid+"@example.com", uid, uid,
		); err != nil {
			t.Fatalf("seed user %s: %v", uid, err)
		}
	}
	return NewRepository(d.DB), users
}

func TestDirectRoomIsReused(t *testing.T) {
	repo, users := testRepo(t)
	ctx := context.Background()
	first, created, err := repo.CreateOrGetDirect(ctx, users[0], users[1])
	if err != nil {
		t.Fatalf("CreateOrGetDirect first: %v", err)
	}
	if !created || first.Kind != KindDirect || len(first.Participants) != 2 {
		t.Fatalf("unexpected first room: created=%v room=%+v", created, first)
	}
	second, created, err := repo.CreateOrGetDirect(ctx, users[1], users[0])
	if err != nil {
		t.Fatalf("CreateOrGetDirect second: %v", err)
	}
	if created || second.ID != first.ID {
		t.Fatalf("direct room was not reused: created=%v second=%+v first=%+v", created, second, first)
	}
}

func TestMessageOwnershipReadState(t *testing.T) {
	repo, users := testRepo(t)
	ctx := context.Background()
	room, _, err := repo.CreateOrGetDirect(ctx, users[0], users[1])
	if err != nil {
		t.Fatalf("CreateOrGetDirect: %v", err)
	}
	msg, recipients, err := repo.AppendMessage(ctx, users[0], room.ID, "hello")
	if err != nil {
		t.Fatalf("AppendMessage: %v", err)
	}
	if msg.Seq != 1 || msg.ReadByCount != 1 {
		t.Fatalf("unexpected message read state: %+v", msg)
	}
	if len(recipients) != 2 {
		t.Fatalf("recipients = %d, want 2", len(recipients))
	}
	rooms, err := repo.ListRooms(ctx, users[1], 50)
	if err != nil {
		t.Fatalf("ListRooms: %v", err)
	}
	if len(rooms) != 1 || rooms[0].UnreadCount != 1 || rooms[0].LastMessage == nil {
		t.Fatalf("unexpected room summary: %+v", rooms)
	}
	if _, _, err := repo.MarkRead(ctx, users[1], room.ID, msg.Seq); err != nil {
		t.Fatalf("MarkRead: %v", err)
	}
	rooms, _ = repo.ListRooms(ctx, users[1], 50)
	if rooms[0].UnreadCount != 0 || rooms[0].LastReadSeq != msg.Seq {
		t.Fatalf("read state did not advance: %+v", rooms[0])
	}
}

func TestForeignUserCannotReadRoom(t *testing.T) {
	repo, users := testRepo(t)
	ctx := context.Background()
	room, _, err := repo.CreateOrGetDirect(ctx, users[0], users[1])
	if err != nil {
		t.Fatalf("CreateOrGetDirect: %v", err)
	}
	if _, err := repo.ListMessages(ctx, users[2], room.ID, 0, 50); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound for foreign room, got %v", err)
	}
}

func TestGroupRenameRequiresOwner(t *testing.T) {
	repo, users := testRepo(t)
	ctx := context.Background()
	room, err := repo.CreateGroup(ctx, users[0], "Planning", users[1:])
	if err != nil {
		t.Fatalf("CreateGroup: %v", err)
	}
	if _, err := repo.RenameRoom(ctx, users[1], room.ID, "Nope"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("expected ErrForbidden, got %v", err)
	}
	renamed, err := repo.RenameRoom(ctx, users[0], room.ID, "Launch")
	if err != nil {
		t.Fatalf("RenameRoom owner: %v", err)
	}
	if renamed.Title != "Launch" {
		t.Fatalf("title = %q, want Launch", renamed.Title)
	}
}

// TestEditDeleteMessageAuthzAndCap verifies only the sender can edit/delete
// their own message, the body cap applies to edits, and delete excludes the
// row from GetMessage.
// TestReplayAcrossRoomsUsesCreatedAt guards against the per-room seq bug:
// seq is only unique within a room, so the replay cursor must be created_at.
// Two rooms each get a seq=1 message; replay from the zero time must return
// both, not skip one whose seq collides with the other.
func TestReplayAcrossRoomsUsesCreatedAt(t *testing.T) {
	repo, users := testRepo(t)
	ctx := context.Background()
	roomA, _, err := repo.CreateOrGetDirect(ctx, users[0], users[1])
	if err != nil {
		t.Fatalf("CreateOrGetDirect A: %v", err)
	}
	roomB, _, err := repo.CreateOrGetDirect(ctx, users[0], users[2])
	if err != nil {
		t.Fatalf("CreateOrGetDirect B: %v", err)
	}
	if _, _, err := repo.AppendMessage(ctx, users[0], roomA.ID, "A1"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := repo.AppendMessage(ctx, users[0], roomB.ID, "B1"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := repo.AppendMessage(ctx, users[0], roomA.ID, "A2"); err != nil {
		t.Fatal(err)
	}

	got, err := repo.ListMessagesSinceAcrossRooms(ctx, users[0], time.Unix(0, 0).UTC(), "", 100)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	// Expect all three messages, despite roomA and roomB sharing seq=1.
	if len(got) != 3 {
		t.Fatalf("replay returned %d messages, want 3 (per-room seq must not lose rows): %+v", len(got), got)
	}
	// Ordering is created_at ASC; tiebreak id ASC. Both seq=1 rows must appear.
	seqs := map[string][]int64{}
	for _, m := range got {
		seqs[m.RoomID] = append(seqs[m.RoomID], m.Seq)
	}
	if len(seqs[roomA.ID]) != 2 || len(seqs[roomB.ID]) != 1 {
		t.Fatalf("per-room replay counts wrong: %+v", seqs)
	}
}

// TestReplayPagesAndSignalsHasMore locks in the round-5 fix: a backlog larger
// than one replay page must set hasMore and advance the (created_at, id)
// cursor so the caller can page through the rest instead of truncating.
func TestReplayPagesAndSignalsHasMore(t *testing.T) {
	repo, users := testRepo(t)
	ctx := context.Background()
	room, _, err := repo.CreateOrGetDirect(ctx, users[0], users[1])
	if err != nil {
		t.Fatalf("CreateOrGetDirect: %v", err)
	}
	// Three messages in the same second (created_at default is now); the id
	// tiebreak must keep them distinct in the cursor.
	for i := 0; i < 3; i++ {
		if _, _, err := repo.AppendMessage(ctx, users[0], room.ID, "m"); err != nil {
			t.Fatalf("AppendMessage %d: %v", i, err)
		}
	}
	svc := NewService(repo, NewHub())
	page1, cur, curID, hasMore, err := svc.ReplayMissed(ctx, users[0], time.Unix(0, 0).UTC(), "", 2)
	if err != nil {
		t.Fatalf("page1: %v", err)
	}
	if len(page1) != 2 || !hasMore {
		t.Fatalf("page1 = %d msgs hasMore=%v, want 2/true", len(page1), hasMore)
	}
	page2, _, _, hasMore2, err := svc.ReplayMissed(ctx, users[0], cur, curID, 2)
	if err != nil {
		t.Fatalf("page2: %v", err)
	}
	if len(page2) != 1 || hasMore2 {
		t.Fatalf("page2 = %d msgs hasMore=%v, want 1/false (tail)", len(page2), hasMore2)
	}
}

func TestEditDeleteMessageAuthzAndCap(t *testing.T) {
	repo, users := testRepo(t)
	ctx := context.Background()
	room, _, err := repo.CreateOrGetDirect(ctx, users[0], users[1])
	if err != nil {
		t.Fatalf("CreateOrGetDirect: %v", err)
	}
	msg, _, err := repo.AppendMessage(ctx, users[0], room.ID, "original")
	if err != nil {
		t.Fatalf("AppendMessage: %v", err)
	}

	// Non-sender cannot edit.
	if _, _, err := repo.EditMessage(ctx, users[1], room.ID, msg.ID, "hijack"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("expected ErrForbidden for non-sender edit, got %v", err)
	}
	// Over-cap body rejected.
	long := make([]byte, maxMessageBody+1)
	for i := range long {
		long[i] = 'x'
	}
	if _, _, err := repo.EditMessage(ctx, users[0], room.ID, msg.ID, string(long)); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("expected ErrInvalidInput for over-cap edit, got %v", err)
	}
	// Sender can edit; body + edited_at update.
	edited, _, err := repo.EditMessage(ctx, users[0], room.ID, msg.ID, "fixed")
	if err != nil {
		t.Fatalf("EditMessage: %v", err)
	}
	if edited.Body != "fixed" || edited.EditedAt == nil {
		t.Fatalf("edited = %+v, want body fixed and edited_at set", edited)
	}
	// Non-sender cannot delete.
	if _, err := repo.DeleteMessage(ctx, users[1], room.ID, msg.ID); !errors.Is(err, ErrForbidden) {
		t.Fatalf("expected ErrForbidden for non-sender delete, got %v", err)
	}
	// Sender delete succeeds; GetMessage then 404s.
	if _, err := repo.DeleteMessage(ctx, users[0], room.ID, msg.ID); err != nil {
		t.Fatalf("DeleteMessage: %v", err)
	}
	if _, err := repo.GetMessage(ctx, users[0], room.ID, msg.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound after delete, got %v", err)
	}
}
