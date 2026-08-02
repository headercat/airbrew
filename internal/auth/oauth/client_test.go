package oauth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/headercat/airbrew/internal/db"
)

func testClientService(t *testing.T) *ClientService {
	t.Helper()
	svc, _ := testClientServiceWithDB(t)
	return svc
}

func testClientServiceWithDB(t *testing.T) (*ClientService, *db.DB) {
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
	return NewClientService(repo), d
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

func TestClientServiceRejectsPlainHTTPRedirectURIExceptLoopback(t *testing.T) {
	svc := testClientService(t)
	_, err := svc.Create(context.Background(), ClientCreate{
		Name:         "Bad Web App",
		ClientType:   ClientTypePublic,
		RedirectURIs: []string{"http://example.com/callback"},
	})
	if !errors.Is(err, ErrInvalidRedirectURI) {
		t.Fatalf("expected ErrInvalidRedirectURI, got %v", err)
	}

	_, err = svc.Create(context.Background(), ClientCreate{
		Name:         "Loopback CLI",
		ClientType:   ClientTypePublic,
		RedirectURIs: []string{"http://127.0.0.1:5051/callback"},
	})
	if err != nil {
		t.Fatalf("expected loopback HTTP redirect to be accepted: %v", err)
	}
}

func TestClientServiceAllowsPrivateUseRedirectURIForPublicClients(t *testing.T) {
	svc := testClientService(t)
	_, err := svc.Create(context.Background(), ClientCreate{
		Name:         "Native App",
		ClientType:   ClientTypePublic,
		RedirectURIs: []string{"com.example.airbrew:/oauth/callback"},
	})
	if err != nil {
		t.Fatalf("expected private-use URI for public client to be accepted: %v", err)
	}

	_, err = svc.Create(context.Background(), ClientCreate{
		Name:         "Server App",
		ClientType:   ClientTypeConfidential,
		RedirectURIs: []string{"com.example.airbrew:/oauth/callback"},
	})
	if !errors.Is(err, ErrInvalidRedirectURI) {
		t.Fatalf("expected ErrInvalidRedirectURI for confidential client, got %v", err)
	}
}

func TestClientServiceRejectsInvalidScopeSyntax(t *testing.T) {
	svc := testClientService(t)
	_, err := svc.Create(context.Background(), ClientCreate{
		Name:          "Bad Scope App",
		ClientType:    ClientTypePublic,
		AllowedScopes: []string{"openid", "bad\"scope"},
		RedirectURIs:  []string{"https://example.com/callback"},
	})
	if !errors.Is(err, ErrInvalidScopeSyntax) {
		t.Fatalf("expected ErrInvalidScopeSyntax, got %v", err)
	}
}

func TestClientServiceRejectsInvalidTokenAuthMethod(t *testing.T) {
	svc := testClientService(t)
	_, err := svc.Create(context.Background(), ClientCreate{
		Name:                    "Bad Auth Method App",
		ClientType:              ClientTypeConfidential,
		TokenEndpointAuthMethod: "private_key_jwt",
		RedirectURIs:            []string{"https://example.com/callback"},
	})
	if !errors.Is(err, ErrInvalidAuthMethod) {
		t.Fatalf("expected ErrInvalidAuthMethod, got %v", err)
	}
}

func TestClientServiceGetByClientIDRequiresActiveClient(t *testing.T) {
	svc := testClientService(t)
	res, err := svc.Create(context.Background(), ClientCreate{
		Name:         "Inactive App",
		ClientType:   ClientTypePublic,
		RedirectURIs: []string{"https://example.com/callback"},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = svc.Update(context.Background(), res.Client.ID, ClientUpdate{
		Name:         "Inactive App",
		RedirectURIs: []string{"https://example.com/callback"},
		IsActive:     false,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = svc.GetByClientID(context.Background(), res.Client.ClientID)
	if !errors.Is(err, ErrClientNotFound) {
		t.Fatalf("expected ErrClientNotFound, got %v", err)
	}
}

func TestClientServiceRevokeIssuedCredentials(t *testing.T) {
	svc, d := testClientServiceWithDB(t)
	ctx := context.Background()
	res, err := svc.Create(ctx, ClientCreate{
		Name:         "Revoked App",
		ClientType:   ClientTypeConfidential,
		RedirectURIs: []string{"https://example.com/callback"},
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	if _, err := d.ExecContext(ctx, `
		INSERT INTO users (id, public_subject, email, status, created_at, updated_at)
		VALUES ('usr_revoked', 'sub_revoked', 'revoked@example.com', 'active', ?, ?)
	`, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := d.ExecContext(ctx, `
		INSERT INTO sessions (id, user_id, session_token_hash, created_at, expires_at)
		VALUES ('sess_revoked', 'usr_revoked', 'hash_revoked', ?, ?)
	`, now, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := d.ExecContext(ctx, `
		INSERT INTO oauth_authorization_codes
			(id, code_hash, oauth_client_id, user_id, session_id, redirect_uri, code_challenge, created_at, expires_at)
		VALUES
			('code_active', 'code_hash_active', ?, 'usr_revoked', 'sess_revoked', 'https://example.com/callback', 'challenge', ?, ?),
			('code_expired', 'code_hash_expired', ?, 'usr_revoked', 'sess_revoked', 'https://example.com/callback', 'challenge', ?, ?)
	`, res.Client.ID, now, now.Add(time.Hour),
		res.Client.ID, now.Add(-2*time.Hour), now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := d.ExecContext(ctx, `
		INSERT INTO oauth_refresh_tokens
			(id, token_hash, family_id, oauth_client_id, user_id, created_at, expires_at)
		VALUES
			('refresh_active', 'refresh_hash_active', 'family_active', ?, 'usr_revoked', ?, ?),
			('refresh_expired', 'refresh_hash_expired', 'family_expired', ?, 'usr_revoked', ?, ?)
	`, res.Client.ID, now, now.Add(time.Hour),
		res.Client.ID, now.Add(-2*time.Hour), now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}

	revoked, err := svc.RevokeIssuedCredentials(ctx, res.Client.ID)
	if err != nil {
		t.Fatal(err)
	}
	if revoked.AuthorizationCodes != 1 || revoked.RefreshTokens != 1 {
		t.Fatalf("revoked = %#v, want one active code and one active refresh token", revoked)
	}

	var consumed, tokenRevoked int
	if err := d.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM oauth_authorization_codes WHERE oauth_client_id = ? AND consumed_at IS NOT NULL",
		res.Client.ID,
	).Scan(&consumed); err != nil {
		t.Fatal(err)
	}
	if err := d.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM oauth_refresh_tokens WHERE oauth_client_id = ? AND revoked_at IS NOT NULL",
		res.Client.ID,
	).Scan(&tokenRevoked); err != nil {
		t.Fatal(err)
	}
	if consumed != 1 || tokenRevoked != 1 {
		t.Fatalf("consumed=%d revoked=%d, want 1/1", consumed, tokenRevoked)
	}
}

func TestClientServiceAuthenticateTokenClientRespectsRegisteredMethod(t *testing.T) {
	svc := testClientService(t)
	res, err := svc.Create(context.Background(), ClientCreate{
		Name:                    "Post Auth App",
		ClientType:              ClientTypeConfidential,
		TokenEndpointAuthMethod: TokenEndpointAuthPost,
		RedirectURIs:            []string{"https://example.com/callback"},
	})
	if err != nil {
		t.Fatal(err)
	}

	_, err = svc.AuthenticateTokenClient(context.Background(), ClientAuthentication{
		ClientID:     res.Client.ClientID,
		ClientSecret: res.ClientSecret,
		Method:       TokenEndpointAuthBasic,
	})
	if !errors.Is(err, ErrInvalidClient) {
		t.Fatalf("expected ErrInvalidClient for method mismatch, got %v", err)
	}

	client, err := svc.AuthenticateTokenClient(context.Background(), ClientAuthentication{
		ClientID:     res.Client.ClientID,
		ClientSecret: res.ClientSecret,
		Method:       TokenEndpointAuthPost,
	})
	if err != nil {
		t.Fatal(err)
	}
	if client.ID != res.Client.ID {
		t.Fatalf("expected client %q, got %q", res.Client.ID, client.ID)
	}
}

func TestClientServiceAuthenticateTokenClientAllowsPublicWithoutSecret(t *testing.T) {
	svc := testClientService(t)
	res, err := svc.Create(context.Background(), ClientCreate{
		Name:         "Public App",
		ClientType:   ClientTypePublic,
		RedirectURIs: []string{"https://example.com/callback"},
	})
	if err != nil {
		t.Fatal(err)
	}
	client, err := svc.ValidatePublicTokenClient(context.Background(), res.Client.ClientID)
	if err != nil {
		t.Fatal(err)
	}
	if client.ID != res.Client.ID {
		t.Fatalf("expected client %q, got %q", res.Client.ID, client.ID)
	}
	_, err = svc.AuthenticateTokenClient(context.Background(), ClientAuthentication{
		ClientID:     res.Client.ClientID,
		ClientSecret: "not-allowed",
		Method:       TokenEndpointAuthPost,
	})
	if !errors.Is(err, ErrPublicClientSecret) {
		t.Fatalf("expected ErrPublicClientSecret, got %v", err)
	}
}

func TestClientAuthenticationFromRequest(t *testing.T) {
	body := url.Values{
		"client_id":     {"airbrew_post"},
		"client_secret": {"secret"},
	}.Encode()
	req := httptest.NewRequest(http.MethodPost, "/oauth/token", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	auth, err := ClientAuthenticationFromRequest(req)
	if err != nil {
		t.Fatal(err)
	}
	if auth.ClientID != "airbrew_post" || auth.ClientSecret != "secret" || auth.Method != TokenEndpointAuthPost {
		t.Fatalf("unexpected auth: %#v", auth)
	}

	req = httptest.NewRequest(http.MethodPost, "/oauth/token", strings.NewReader("client_id=airbrew_public"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth("airbrew_basic", "secret")
	_, err = ClientAuthenticationFromRequest(req)
	if !errors.Is(err, ErrInvalidClient) {
		t.Fatalf("expected ErrInvalidClient for duplicate auth, got %v", err)
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

func TestClientServiceGetDoesNotBlockSingleSQLiteConnection(t *testing.T) {
	svc := testClientService(t)
	res, err := svc.Create(context.Background(), ClientCreate{
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
	client, err := svc.Get(ctx, res.Client.ID)
	if err != nil {
		t.Fatal(err)
	}
	if client.ID != res.Client.ID {
		t.Fatalf("expected client %q, got %q", res.Client.ID, client.ID)
	}
	if len(client.RedirectURIs) != 1 || client.RedirectURIs[0] != "https://example.com/callback" {
		t.Fatalf("unexpected redirect URIs: %#v", client.RedirectURIs)
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
