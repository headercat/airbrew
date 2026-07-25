package oauth

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/headercat/airbrew/internal/db"
)

func testClientService(t *testing.T) *ClientService {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "airbrew-test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	if err := d.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	repo := NewClientRepository(d.DB)
	return NewClientService(repo)
}

func TestClientServiceCreateConfidential(t *testing.T) {
	svc := testClientService(t)
	res, err := svc.Create(context.Background(), ClientCreate{
		Name:          "Example App",
		ClientType:    ClientTypeConfidential,
		AllowedScopes: []string{"openid profile", "email", "openid"},
		RedirectURIs:  []string{"https://example.com/callback"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.ClientSecret == "" {
		t.Fatal("expected one-time client secret")
	}
	if res.Client.ClientSecretHash == "" || res.Client.ClientSecretHash == res.ClientSecret {
		t.Fatal("expected hashed secret storage")
	}
	if res.Client.TokenEndpointAuthMethod != TokenEndpointAuthBasic {
		t.Fatalf("auth method = %q", res.Client.TokenEndpointAuthMethod)
	}
	if got := res.Client.AllowedScopes; len(got) != 3 || got[0] != "email" || got[1] != "openid" || got[2] != "profile" {
		t.Fatalf("unexpected scopes: %#v", got)
	}
}

func TestClientServiceRejectsInvalidRedirectURI(t *testing.T) {
	svc := testClientService(t)
	_, err := svc.Create(context.Background(), ClientCreate{
		Name:         "Bad App",
		ClientType:   ClientTypePublic,
		RedirectURIs: []string{"urn:ietf:wg:oauth:2.0:oob"},
	})
	if err == nil {
		t.Fatal("expected validation error")
	}
}

func TestClientServiceListDoesNotBlockSingleSQLiteConnection(t *testing.T) {
	svc := testClientService(t)
	_, err := svc.Create(context.Background(), ClientCreate{
		Name:          "Example App",
		ClientType:    ClientTypePublic,
		AllowedScopes: []string{"openid"},
		RedirectURIs:  []string{"https://example.com/callback"},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	clients, total, err := svc.List(ctx, 50, 0)
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || len(clients) != 1 {
		t.Fatalf("expected one client, total=%d len=%d", total, len(clients))
	}
	if len(clients[0].RedirectURIs) != 1 || clients[0].RedirectURIs[0] != "https://example.com/callback" {
		t.Fatalf("unexpected redirect URIs: %#v", clients[0].RedirectURIs)
	}
}

func TestClientServiceUpdateAndDelete(t *testing.T) {
	svc := testClientService(t)
	res, err := svc.Create(context.Background(), ClientCreate{
		Name:           "CLI",
		ClientType:     ClientTypePublic,
		RedirectURIs:   []string{"http://localhost:3000/callback"},
		RequireConsent: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	updated, err := svc.Update(context.Background(), res.Client.ID, ClientUpdate{
		Name:           "CLI Updated",
		AllowedScopes:  []string{"openid"},
		RedirectURIs:   []string{"http://localhost:5051/oauth/callback"},
		IsActive:       false,
		RequireConsent: false,
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Name != "CLI Updated" || updated.IsActive {
		t.Fatalf("unexpected update: %#v", updated)
	}
	if len(updated.RedirectURIs) != 1 || updated.RedirectURIs[0] != "http://localhost:5051/oauth/callback" {
		t.Fatalf("unexpected redirect URIs: %#v", updated.RedirectURIs)
	}
	if err := svc.Delete(context.Background(), res.Client.ID); err != nil {
		t.Fatal(err)
	}
	_, err = svc.Get(context.Background(), res.Client.ID)
	if !errors.Is(err, ErrClientNotFound) {
		t.Fatalf("expected ErrClientNotFound, got %v", err)
	}
}
