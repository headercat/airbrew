package security

import (
	"context"
	"database/sql"
	"testing"

	_ "modernc.org/sqlite"
)

func TestAllowsIP(t *testing.T) {
	ctx := context.Background()
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
