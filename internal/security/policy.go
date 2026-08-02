// Package security holds workspace-wide security policy: the configurable
// password policy and the IP allowlist used by the admin security center.
//
// Policy is stored in server_settings as JSON under well-known keys so the
// admin UI can read/write it without schema migrations. The user service
// consumes the policy via the PasswordPolicyChecker interface so it stays
// decoupled from this package.
package security

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode"
)

// PasswordPolicy describes the rules every password must satisfy. It is stored
// as JSON under the server_settings key security.password_policy.
type PasswordPolicy struct {
	MinLength        int  `json:"min_length"`
	RequireUppercase bool `json:"require_uppercase"`
	RequireLowercase bool `json:"require_lowercase"`
	RequireDigit     bool `json:"require_digit"`
	RequireSymbol    bool `json:"require_symbol"`
	// MaxAgeDays forces rotation when a password is older than this many days.
	// 0 disables max-age enforcement.
	MaxAgeDays int `json:"max_age_days"`
	// HistoryCount prevents reusing the last N passwords. 0 disables history
	// enforcement. The history itself is stored per-user; this package only
	// exposes the configured bound.
	HistoryCount int `json:"history_count"`
}

// DefaultPasswordPolicy is applied when no policy is stored yet. It mirrors the
// hard-coded rules the codebase already enforced (>= 8 chars) so existing
// deployments see no behavior change until an admin tightens the policy.
func DefaultPasswordPolicy() PasswordPolicy {
	return PasswordPolicy{MinLength: 8}
}

// settingKey is the server_settings key for the password policy.
const passwordPolicyKey = "security.password_policy"

// Validate returns nil if plain satisfies the policy, otherwise an error whose
// message is safe to surface to the end user.
func (p PasswordPolicy) Validate(plain string) error {
	if p.MinLength > 0 && len(plain) < p.MinLength {
		return ErrPasswordTooShort{Min: p.MinLength}
	}
	var hasUpper, hasLower, hasDigit, hasSymbol bool
	for _, r := range plain {
		switch {
		case unicode.IsUpper(r):
			hasUpper = true
		case unicode.IsLower(r):
			hasLower = true
		case unicode.IsDigit(r):
			hasDigit = true
		case unicode.IsSymbol(r), unicode.IsPunct(r):
			hasSymbol = true
		}
	}
	if p.RequireUppercase && !hasUpper {
		return errors.New("password must contain at least one uppercase letter")
	}
	if p.RequireLowercase && !hasLower {
		return errors.New("password must contain at least one lowercase letter")
	}
	if p.RequireDigit && !hasDigit {
		return errors.New("password must contain at least one digit")
	}
	if p.RequireSymbol && !hasSymbol {
		return errors.New("password must contain at least one symbol")
	}
	return nil
}

// ErrPasswordTooShort is returned when a password is shorter than the policy
// minimum. It carries the required minimum so callers can build precise
// messages.
type ErrPasswordTooShort struct{ Min int }

func (e ErrPasswordTooShort) Error() string {
	return "password must be at least " + itoa(e.Min) + " characters"
}

// Checker is the slice of security.Service the user package consumes. It is
// kept as an interface so the user package does not import security.
type Checker interface {
	PasswordPolicy(ctx context.Context) (PasswordPolicy, error)
	ValidatePassword(ctx context.Context, plain string) error
}

// Service loads and persists security policy from server_settings.
type Service struct {
	db *sql.DB

	mu     sync.RWMutex
	cached PasswordPolicy
	dirty  bool
}

// NewService returns a Service bound to db. The policy is loaded lazily on the
// first read so the constructor never fails.
func NewService(db *sql.DB) *Service {
	return &Service{db: db, cached: DefaultPasswordPolicy(), dirty: true}
}

// PasswordPolicy returns the effective password policy, loading it from the
// database on first use and caching the result.
func (s *Service) PasswordPolicy(ctx context.Context) (PasswordPolicy, error) {
	s.mu.RLock()
	if !s.dirty {
		p := s.cached
		s.mu.RUnlock()
		return p, nil
	}
	s.mu.RUnlock()

	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.dirty {
		return s.cached, nil
	}
	p := DefaultPasswordPolicy()
	var raw string
	err := s.db.QueryRowContext(ctx,
		"SELECT value FROM server_settings WHERE key = ?", passwordPolicyKey,
	).Scan(&raw)
	if err == nil && raw != "" {
		var stored PasswordPolicy
		if jerr := json.Unmarshal([]byte(raw), &stored); jerr == nil {
			p = normalizePolicy(stored)
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return p, err
	}
	s.cached = p
	s.dirty = false
	return p, nil
}

// SetPasswordPolicy persists p and refreshes the in-memory cache.
func (s *Service) SetPasswordPolicy(ctx context.Context, p PasswordPolicy) error {
	p = normalizePolicy(p)
	raw, err := json.Marshal(p)
	if err != nil {
		return err
	}
	now := time.Now().UTC().Truncate(time.Second)
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO server_settings (key, value, updated_at) VALUES (?, ?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at
	`, passwordPolicyKey, string(raw), now)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.cached = p
	s.dirty = false
	s.mu.Unlock()
	return nil
}

// ValidatePassword implements Checker. It loads the current policy and applies
// it to plain.
func (s *Service) ValidatePassword(ctx context.Context, plain string) error {
	p, err := s.PasswordPolicy(ctx)
	if err != nil {
		return err
	}
	return p.Validate(plain)
}

// PasswordHistoryCount returns the configured password reuse prevention depth.
func (s *Service) PasswordHistoryCount(ctx context.Context) (int, error) {
	p, err := s.PasswordPolicy(ctx)
	if err != nil {
		return 0, err
	}
	return p.HistoryCount, nil
}

// NormalizePasswordPolicy clamps the policy to safe bounds. Exported so the
// admin handler can echo a normalized value back to the UI.
func NormalizePasswordPolicy(p PasswordPolicy) PasswordPolicy { return normalizePolicy(p) }

func normalizePolicy(p PasswordPolicy) PasswordPolicy {
	if p.MinLength < 1 {
		p.MinLength = 8
	}
	if p.MinLength > 128 {
		p.MinLength = 128
	}
	if p.MaxAgeDays < 0 {
		p.MaxAgeDays = 0
	}
	if p.MaxAgeDays > 3650 {
		p.MaxAgeDays = 3650
	}
	if p.HistoryCount < 0 {
		p.HistoryCount = 0
	}
	if p.HistoryCount > 24 {
		p.HistoryCount = 24
	}
	return p
}

// IPAllowlist is the optional set of CIDRs the session middleware enforces.
type IPAllowlist struct {
	Enabled        bool     `json:"enabled"`
	CIDRs          []string `json:"cidrs"`
	TrustedProxies []string `json:"trusted_proxies"`
}

const ipAllowlistKey = "security.ip_allowlist"

// DefaultIPAllowlist returns an empty, disabled allowlist.
func DefaultIPAllowlist() IPAllowlist { return IPAllowlist{CIDRs: []string{}} }

// IPAllowlist loads the stored allowlist, returning the default when unset.
func (s *Service) IPAllowlist(ctx context.Context) (IPAllowlist, error) {
	out := DefaultIPAllowlist()
	var raw string
	err := s.db.QueryRowContext(ctx,
		"SELECT value FROM server_settings WHERE key = ?", ipAllowlistKey,
	).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return out, nil
	}
	if err != nil {
		return out, err
	}
	if raw != "" {
		_ = json.Unmarshal([]byte(raw), &out)
	}
	if out.CIDRs == nil {
		out.CIDRs = []string{}
	}
	if out.TrustedProxies == nil {
		out.TrustedProxies = []string{}
	}
	return out, nil
}

// SetIPAllowlist persists the allowlist.
func (s *Service) SetIPAllowlist(ctx context.Context, a IPAllowlist) error {
	a.CIDRs = normalizeCIDRs(a.CIDRs)
	a.TrustedProxies = normalizeCIDRs(a.TrustedProxies)
	raw, err := json.Marshal(a)
	if err != nil {
		return err
	}
	now := time.Now().UTC().Truncate(time.Second)
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO server_settings (key, value, updated_at) VALUES (?, ?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at
	`, ipAllowlistKey, string(raw), now)
	return err
}

// AllowsIP reports whether rawIP is allowed by the configured allowlist. When
// the allowlist is disabled, every address is accepted.
func (s *Service) AllowsIP(ctx context.Context, rawIP string) (bool, error) {
	a, err := s.IPAllowlist(ctx)
	if err != nil {
		return false, err
	}
	if !a.Enabled {
		return true, nil
	}
	return IPMatches(rawIP, a.CIDRs), nil
}

// RequestIP resolves the client IP for security decisions. Forwarded headers
// are used only when the direct peer is explicitly configured as trusted.
func (s *Service) RequestIP(ctx context.Context, r *http.Request) (string, error) {
	a, err := s.IPAllowlist(ctx)
	if err != nil {
		return "", err
	}
	return ResolveRequestIP(r, a), nil
}

// ResolveRequestIP applies trusted-proxy client IP resolution to r.
func ResolveRequestIP(r *http.Request, a IPAllowlist) string {
	direct := directRequestIP(r)
	if !IPMatches(direct, a.TrustedProxies) {
		return direct
	}
	if f := firstForwardedFor(r.Header.Get("Forwarded")); f != "" {
		return f
	}
	if f := r.Header.Get("X-Forwarded-For"); f != "" {
		parts := strings.Split(f, ",")
		if len(parts) > 0 {
			if ip := strings.TrimSpace(parts[0]); ip != "" {
				return ip
			}
		}
	}
	return direct
}

// IPMatches reports whether rawIP is contained in any IP or CIDR entry.
func IPMatches(rawIP string, cidrs []string) bool {
	host, _, err := net.SplitHostPort(rawIP)
	if err == nil {
		rawIP = host
	}
	ip := net.ParseIP(strings.TrimSpace(rawIP))
	if ip == nil {
		return false
	}
	for _, allowed := range cidrs {
		allowed = strings.TrimSpace(allowed)
		if strings.Contains(allowed, "/") {
			_, network, err := net.ParseCIDR(allowed)
			if err == nil && network.Contains(ip) {
				return true
			}
			continue
		}
		if other := net.ParseIP(allowed); other != nil && other.Equal(ip) {
			return true
		}
	}
	return false
}

func directRequestIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil {
		return host
	}
	return strings.TrimSpace(r.RemoteAddr)
}

func firstForwardedFor(raw string) string {
	for _, part := range strings.Split(raw, ",") {
		for _, kv := range strings.Split(part, ";") {
			k, v, ok := strings.Cut(strings.TrimSpace(kv), "=")
			if !ok || strings.ToLower(strings.TrimSpace(k)) != "for" {
				continue
			}
			v = strings.Trim(strings.TrimSpace(v), `"`)
			if strings.HasPrefix(v, "[") {
				if end := strings.Index(v, "]"); end >= 0 {
					v = v[1:end]
				}
			} else if host, _, err := net.SplitHostPort(v); err == nil {
				v = host
			}
			if v != "" {
				return v
			}
		}
	}
	return ""
}

func normalizeCIDRs(in []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, c := range in {
		c = strings.TrimSpace(c)
		if c == "" || seen[c] {
			continue
		}
		seen[c] = true
		out = append(out, c)
	}
	return out
}

// itoa avoids importing strconv just for one int-to-string call.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
