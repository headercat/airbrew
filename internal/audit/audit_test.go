package audit

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/headercat/airbrew/internal/db"
)

func TestListFiltersActorByEmailAndClientID(t *testing.T) {
	ctx := context.Background()
	d, err := db.Open(filepath.Join(t.TempDir(), "airbrew-test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	if err := d.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := d.ExecContext(ctx, `
		INSERT INTO users (id, public_subject, email, status)
		VALUES ('usr_audit', 'sub_audit', 'admin@example.com', 'active');
		INSERT INTO oauth_clients
			(id, client_id, name, client_type, token_endpoint_auth_method, allowed_scopes)
		VALUES
			('oauth_audit', 'airbrew_audit', 'Audit Client', 'public', 'none', '');
	`); err != nil {
		t.Fatal(err)
	}
	svc := NewService(d.DB)
	svc.Log(ctx, Entry{EventType: "user.created", ActorUserID: "usr_audit"})
	svc.Log(ctx, Entry{EventType: "oauth.event", ActorClientID: "oauth_audit"})

	entries, total, err := svc.List(ctx, ListFilter{ActorID: "admin@example.com"})
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || len(entries) != 1 || entries[0].ActorEmail != "admin@example.com" {
		t.Fatalf("email actor filter total=%d entries=%#v", total, entries)
	}

	entries, total, err = svc.List(ctx, ListFilter{ActorID: "admin@"})
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || len(entries) != 1 || entries[0].ActorUserID != "usr_audit" {
		t.Fatalf("partial email actor filter total=%d entries=%#v", total, entries)
	}

	entries, total, err = svc.List(ctx, ListFilter{ActorID: "oauth_audit"})
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || len(entries) != 1 || entries[0].ActorClientID != "oauth_audit" {
		t.Fatalf("client actor filter total=%d entries=%#v", total, entries)
	}
}
