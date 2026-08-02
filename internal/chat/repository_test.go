package chat

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

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
