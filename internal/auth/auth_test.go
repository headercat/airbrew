package auth

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/headercat/airbrew/internal/security"

	_ "modernc.org/sqlite"
)

func TestSessionMiddlewareEnforcesIPAllowlist(t *testing.T) {
	ctx := context.Background()
	securitySvc := newTestSecurityService(t, ctx)
	if err := securitySvc.SetIPAllowlist(ctx, security.IPAllowlist{
		Enabled: true,
		CIDRs:   []string{"203.0.113.7"},
	}); err != nil {
		t.Fatal(err)
	}
	module := &Module{Security: securitySvc}

	nextCalled := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		nextCalled = true
		w.WriteHeader(http.StatusNoContent)
	})

	req := httptest.NewRequest(http.MethodGet, "/api/auth/me", nil)
	req.RemoteAddr = "203.0.113.8:44121"
	rec := httptest.NewRecorder()
	module.SessionMiddleware(next).ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusForbidden)
	}
	if nextCalled {
		t.Fatal("next handler should not run for a blocked IP")
	}

	req = httptest.NewRequest(http.MethodGet, "/api/auth/me", nil)
	req.RemoteAddr = "203.0.113.7:44121"
	rec = httptest.NewRecorder()
	module.SessionMiddleware(next).ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNoContent)
	}
}

func TestSessionMiddlewareIgnoresForwardedForForAllowlist(t *testing.T) {
	ctx := context.Background()
	securitySvc := newTestSecurityService(t, ctx)
	if err := securitySvc.SetIPAllowlist(ctx, security.IPAllowlist{
		Enabled: true,
		CIDRs:   []string{"203.0.113.7"},
	}); err != nil {
		t.Fatal(err)
	}
	module := &Module{Security: securitySvc}

	req := httptest.NewRequest(http.MethodGet, "/api/auth/me", nil)
	req.RemoteAddr = "203.0.113.8:44121"
	req.Header.Set("X-Forwarded-For", "203.0.113.7")
	rec := httptest.NewRecorder()
	module.SessionMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})).ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusForbidden)
	}
}

func newTestSecurityService(t *testing.T, ctx context.Context) *security.Service {
	t.Helper()
	db, err := sql.Open("sqlite", "file::memory:?cache=shared&_pragma=foreign_keys(1)&_time_format=sqlite")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.ExecContext(ctx, `
		CREATE TABLE server_settings (
			key TEXT PRIMARY KEY,
			value TEXT NOT NULL,
			updated_at DATETIME NOT NULL
		)
	`); err != nil {
		t.Fatal(err)
	}
	return security.NewService(db)
}
