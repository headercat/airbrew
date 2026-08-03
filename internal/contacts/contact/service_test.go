package contact

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/headercat/airbrew/internal/db"
)

func testService(t *testing.T) (*Service, string) {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "contacts-test.db"))
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
	return NewService(NewRepository(d.DB), nil), uid
}

func TestListContactsIncludesGroupsForMultipleContacts(t *testing.T) {
	svc, uid := testService(t)
	ctx := context.Background()
	group, err := svc.CreateGroup(ctx, CreateGroupInput{UserID: uid, Name: "Work"})
	if err != nil {
		t.Fatal(err)
	}
	first, err := svc.Create(ctx, CreateContactInput{UserID: uid, DisplayName: "Ada", GroupIDs: []string{group.ID}})
	if err != nil {
		t.Fatal(err)
	}
	second, err := svc.Create(ctx, CreateContactInput{UserID: uid, DisplayName: "Grace", GroupIDs: []string{group.ID}})
	if err != nil {
		t.Fatal(err)
	}

	groupMap, err := svc.GroupsForContacts(ctx, uid, []string{first.ID, second.ID})
	if err != nil {
		t.Fatal(err)
	}
	if got := groupMap[first.ID]; len(got) != 1 || got[0] != group.ID {
		t.Fatalf("expected first contact group %q, got %#v", group.ID, got)
	}
	if got := groupMap[second.ID]; len(got) != 1 || got[0] != group.ID {
		t.Fatalf("expected second contact group %q, got %#v", group.ID, got)
	}
}

func TestPatchNormalizesAndRejectsBlankIdentity(t *testing.T) {
	svc, uid := testService(t)
	ctx := context.Background()
	c, err := svc.Create(ctx, CreateContactInput{
		UserID: uid,
		Emails: []Email{{Value: " ada@example.com ", Type: " work "}},
	})
	if err != nil {
		t.Fatal(err)
	}

	note := "  keep tidy  "
	email := []Email{{Value: " updated@example.com "}, {Value: "   "}}
	updated, err := svc.Patch(ctx, uid, c.ID, ContactPatch{Emails: &email, Notes: &note})
	if err != nil {
		t.Fatal(err)
	}
	if len(updated.Emails) != 1 || updated.Emails[0].Value != "updated@example.com" {
		t.Fatalf("expected cleaned email, got %#v", updated.Emails)
	}
	if updated.Notes != "keep tidy" {
		t.Fatalf("expected trimmed notes, got %q", updated.Notes)
	}

	emptyEmails := []Email{}
	if _, err := svc.Patch(ctx, uid, c.ID, ContactPatch{Emails: &emptyEmails}); !errors.Is(err, ErrNameRequired) {
		t.Fatalf("expected ErrNameRequired when clearing only identity, got %v", err)
	}
}

func TestExportUsesLargerLimit(t *testing.T) {
	svc, uid := testService(t)
	ctx := context.Background()
	for i := 0; i < 250; i++ {
		if _, err := svc.Create(ctx, CreateContactInput{UserID: uid, DisplayName: strings.Repeat("x", 1) + nextID()}); err != nil {
			t.Fatal(err)
		}
	}

	listed, err := svc.List(ctx, ListFilter{UserID: uid, Limit: 500})
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 200 {
		t.Fatalf("expected API list cap of 200, got %d", len(listed))
	}
	exported, err := svc.Export(ctx, uid, "name")
	if err != nil {
		t.Fatal(err)
	}
	if len(exported) != 250 {
		t.Fatalf("expected export to include all contacts, got %d", len(exported))
	}
}

func TestImportDedupsByUIDAndResolvesGroups(t *testing.T) {
	svc, uid := testService(t)
	ctx := context.Background()
	inputs := []CreateContactInput{
		{UID: "dup-1", DisplayName: "Ada", GroupNames: []string{"Work", "Unknown"}},
	}
	summary := svc.ImportContacts(ctx, uid, inputs)
	if summary.Created != 1 || summary.Updated != 0 || summary.Failed != 0 {
		t.Fatalf("first import: %+v", summary)
	}
	// Re-import the same UID with updated fields: should update in place.
	summary = svc.ImportContacts(ctx, uid, []CreateContactInput{
		{UID: "dup-1", DisplayName: "Ada Lovelace", GroupNames: []string{"Work"}},
	})
	if summary.Created != 0 || summary.Updated != 1 || summary.Failed != 0 {
		t.Fatalf("second import: %+v", summary)
	}

	list, err := svc.List(ctx, ListFilter{UserID: uid})
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("dedup should yield 1 contact, got %d", len(list))
	}
	if list[0].DisplayName != "Ada Lovelace" {
		t.Errorf("display name not updated: %q", list[0].DisplayName)
	}
	// "Work" was auto-created; "Unknown" too; both memberships should be present
	// on the first import, then trimmed to just "Work" on the update.
	groups, err := svc.ListGroups(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, g := range groups {
		names[g.Name] = true
	}
	if !names["Work"] || !names["Unknown"] {
		t.Errorf("expected groups Work and Unknown to be created, got %v", names)
	}
	gids, err := svc.GroupsFor(ctx, uid, list[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(gids) != 1 {
		t.Fatalf("after update, expected 1 group membership, got %v", gids)
	}
}

func TestValidationRejectsOversizedInput(t *testing.T) {
	svc, uid := testService(t)
	ctx := context.Background()
	// Oversized notes should be rejected.
	_, err := svc.Create(ctx, CreateContactInput{
		UserID:      uid,
		DisplayName: "ok",
		Notes:       strings.Repeat("n", maxNotesLen+1),
	})
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("expected ErrInvalidInput for oversized notes, got %v", err)
	}
	// Too many emails.
	tooMany := make([]Email, maxValueListLen+1)
	for i := range tooMany {
		tooMany[i] = Email{Value: "a@b.co"}
	}
	_, err = svc.Create(ctx, CreateContactInput{UserID: uid, Emails: tooMany})
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("expected ErrInvalidInput for too many emails, got %v", err)
	}
}

func TestGroupColorValidation(t *testing.T) {
	svc, uid := testService(t)
	ctx := context.Background()
	if _, err := svc.CreateGroup(ctx, CreateGroupInput{UserID: uid, Name: "Bad", Color: "red"}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("expected ErrInvalidInput for non-hex color, got %v", err)
	}
	g, err := svc.CreateGroup(ctx, CreateGroupInput{UserID: uid, Name: "Good", Color: "#ff0000"})
	if err != nil {
		t.Fatalf("valid hex color should pass: %v", err)
	}
	if g.Color != "#ff0000" {
		t.Errorf("color stored as %q, want #ff0000", g.Color)
	}
}
