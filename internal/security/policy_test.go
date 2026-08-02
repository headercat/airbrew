package security

import (
	"context"
	"database/sql"
	"net/http/httptest"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func TestAllowsIP(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t, ctx)
	svc := NewService(db)

	ok, err := svc.AllowsIP(ctx, "203.0.113.10")
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("disabled allowlist should allow every IP")
	}

	if err := svc.SetIPAllowlist(ctx, IPAllowlist{
		Enabled: true,
		CIDRs:   []string{"192.0.2.0/24", "203.0.113.7"},
	}); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		ip   string
		want bool
	}{
		{ip: "192.0.2.10", want: true},
		{ip: "203.0.113.7", want: true},
		{ip: "203.0.113.8", want: false},
		{ip: "not-an-ip", want: false},
	}
	for _, tc := range cases {
		got, err := svc.AllowsIP(ctx, tc.ip)
		if err != nil {
			t.Fatalf("AllowsIP(%q): %v", tc.ip, err)
		}
		if got != tc.want {
			t.Fatalf("AllowsIP(%q) = %v, want %v", tc.ip, got, tc.want)
		}
	}
}

func TestCheckLoginRateLimit(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t, ctx)
	svc := NewService(db)
	now := time.Now().UTC().Truncate(time.Second)
	for i := 0; i < loginRateLimitThreshold; i++ {
		if _, err := db.ExecContext(ctx, `
			INSERT INTO login_attempts (id, email, success, ip_address, failure, created_at)
			VALUES (?, ?, 0, ?, 'invalid_credentials', ?)
		`, itoa(i+1), "USER@example.com", "203.0.113.10", now.Add(time.Duration(-i)*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}

	limit, err := svc.CheckLoginRateLimit(ctx, "user@example.com", "203.0.113.10")
	if err != nil {
		t.Fatal(err)
	}
	if !limit.Blocked {
		t.Fatal("expected repeated failures for the same email and IP to be blocked")
	}
	if !limit.RetryAfter.After(now) {
		t.Fatalf("retry_after = %s, want after %s", limit.RetryAfter, now)
	}

	limit, err = svc.CheckLoginRateLimit(ctx, "user@example.com", "203.0.113.11")
	if err != nil {
		t.Fatal(err)
	}
	if limit.Blocked {
		t.Fatal("failures from one IP should not block a different IP")
	}
}

func TestResolveRequestIPUsesForwardedHeadersOnlyForTrustedProxies(t *testing.T) {
	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "198.51.100.10:443"
	req.Header.Set("X-Forwarded-For", "203.0.113.7")

	if got := ResolveRequestIP(req, IPAllowlist{}); got != "198.51.100.10" {
		t.Fatalf("untrusted proxy resolved IP = %q, want direct peer", got)
	}

	got := ResolveRequestIP(req, IPAllowlist{TrustedProxies: []string{"198.51.100.10"}})
	if got != "203.0.113.7" {
		t.Fatalf("trusted proxy resolved IP = %q, want forwarded client", got)
	}

	req.Header.Set("Forwarded", `for="[2001:db8::7]:443"`)
	got = ResolveRequestIP(req, IPAllowlist{TrustedProxies: []string{"198.51.100.0/24"}})
	if got != "2001:db8::7" {
		t.Fatalf("trusted proxy Forwarded IP = %q, want IPv6 client", got)
	}
}

func newTestDB(t *testing.T, ctx context.Context) *sql.DB {
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
		);
		CREATE TABLE login_attempts (
			id         TEXT PRIMARY KEY NOT NULL,
			user_id    TEXT,
			email      TEXT NOT NULL,
			success    INTEGER NOT NULL DEFAULT 0,
			ip_address TEXT,
			user_agent TEXT,
			failure    TEXT,
			created_at DATETIME NOT NULL
		);
	`); err != nil {
		t.Fatal(err)
	}
	return db
}
