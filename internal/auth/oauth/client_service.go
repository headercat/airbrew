package oauth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"net"
	"net/url"
	"sort"
	"strings"

	"github.com/headercat/airbrew/internal/id"
)

// ClientService contains OAuth client registration rules.
type ClientService struct {
	repo *ClientRepository
}

// NewClientService returns a service backed by repo.
func NewClientService(repo *ClientRepository) *ClientService { return &ClientService{repo: repo} }

// ClientCreate describes a new OAuth client.
type ClientCreate struct {
	Name                    string
	ClientType              ClientType
	TokenEndpointAuthMethod TokenEndpointAuthMethod
	AllowedScopes           []string
	RedirectURIs            []string
	PostLogoutRedirectURIs  []string
	IsFirstParty            bool
	RequireConsent          bool
}

// ClientUpdate describes editable OAuth client fields.
type ClientUpdate struct {
	Name                    string
	TokenEndpointAuthMethod TokenEndpointAuthMethod
	AllowedScopes           []string
	RedirectURIs            []string
	PostLogoutRedirectURIs  []string
	IsFirstParty            bool
	RequireConsent          bool
	IsActive                bool
}

// ClientCreateResult includes a one-time client secret for confidential clients.
type ClientCreateResult struct {
	Client       *Client
	ClientSecret string
}

// ClientAuthentication is the token endpoint's client authentication input.
type ClientAuthentication struct {
	ClientID     string
	ClientSecret string
	Method       TokenEndpointAuthMethod
}

// Create validates and persists a client. Confidential client secrets are
// returned once and stored as SHA-256 hashes.
func (s *ClientService) Create(ctx context.Context, in ClientCreate) (*ClientCreateResult, error) {
	c := &Client{
		ID:                     id.New(),
		ClientID:               "airbrew_" + randomToken(18),
		Name:                   strings.TrimSpace(in.Name),
		ClientType:             in.ClientType,
		AllowedScopes:          normalizeScopes(in.AllowedScopes),
		RedirectURIs:           normalizeURIs(in.RedirectURIs),
		PostLogoutRedirectURIs: normalizeURIs(in.PostLogoutRedirectURIs),
		IsFirstParty:           in.IsFirstParty,
		RequireConsent:         in.RequireConsent,
		IsActive:               true,
	}
	if c.ClientType == "" {
		c.ClientType = ClientTypePublic
	}
	c.TokenEndpointAuthMethod = normalizeAuthMethod(c.ClientType, in.TokenEndpointAuthMethod)
	if err := validateClient(c); err != nil {
		return nil, err
	}

	var secret string
	if c.ClientType == ClientTypeConfidential {
		secret = randomToken(32)
		c.ClientSecretHash = hashSecret(secret)
	}
	if err := s.repo.Create(ctx, c); err != nil {
		return nil, err
	}
	return &ClientCreateResult{Client: c, ClientSecret: secret}, nil
}

// List returns clients.
func (s *ClientService) List(ctx context.Context, limit, offset int) ([]*Client, int, error) {
	clients, err := s.repo.List(ctx, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	total, err := s.repo.Count(ctx)
	if err != nil {
		return nil, 0, err
	}
	return clients, total, nil
}

// Get returns one client.
func (s *ClientService) Get(ctx context.Context, id string) (*Client, error) {
	return s.repo.GetByID(ctx, id)
}

// GetByClientID resolves one active client by its public OAuth client_id.
// The authorize and token endpoints use this (never the internal ID).
func (s *ClientService) GetByClientID(ctx context.Context, clientID string) (*Client, error) {
	return s.repo.GetByClientID(ctx, clientID)
}

// Update validates and persists editable client fields.
func (s *ClientService) Update(ctx context.Context, id string, in ClientUpdate) (*Client, error) {
	c, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	c.Name = strings.TrimSpace(in.Name)
	c.TokenEndpointAuthMethod = normalizeAuthMethod(c.ClientType, in.TokenEndpointAuthMethod)
	c.AllowedScopes = normalizeScopes(in.AllowedScopes)
	c.RedirectURIs = normalizeURIs(in.RedirectURIs)
	c.PostLogoutRedirectURIs = normalizeURIs(in.PostLogoutRedirectURIs)
	c.IsFirstParty = in.IsFirstParty
	c.RequireConsent = in.RequireConsent
	c.IsActive = in.IsActive
	if err := validateClient(c); err != nil {
		return nil, err
	}
	if err := s.repo.Update(ctx, c); err != nil {
		return nil, err
	}
	return s.repo.GetByID(ctx, id)
}

// Delete soft-deletes a client.
func (s *ClientService) Delete(ctx context.Context, id string) error {
	return s.repo.Delete(ctx, id)
}

// RotateSecret issues a fresh one-time secret for a confidential client,
// replacing the stored hash. It returns the plaintext secret exactly once.
// Rotating a public client is an error (ErrPublicClientSecret).
func (s *ClientService) RotateSecret(ctx context.Context, id string) (string, error) {
	c, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return "", err
	}
	if c.ClientType != ClientTypeConfidential {
		return "", ErrPublicClientSecret
	}
	secret := randomToken(32)
	if err := s.repo.UpdateSecret(ctx, id, hashSecret(secret)); err != nil {
		return "", err
	}
	return secret, nil
}

// VerifyClientSecret authenticates a confidential client using
// client_secret_basic semantics. It is kept as a compact helper for callers
// that already know the auth method; token endpoints should prefer
// AuthenticateTokenClient with ClientAuthenticationFromRequest.
func (s *ClientService) VerifyClientSecret(ctx context.Context, clientID, secret string) (*Client, error) {
	return s.AuthenticateTokenClient(ctx, ClientAuthentication{
		ClientID:     clientID,
		ClientSecret: secret,
		Method:       TokenEndpointAuthBasic,
	})
}

// AuthenticateTokenClient applies OAuth token endpoint client authentication
// policy. It accepts unauthenticated public clients, requires confidential
// clients to use their registered auth method, and collapses unknown clients,
// inactive clients, method mismatches, and bad secrets into ErrInvalidClient.
func (s *ClientService) AuthenticateTokenClient(ctx context.Context, in ClientAuthentication) (*Client, error) {
	method := in.Method
	if method == "" {
		method = TokenEndpointAuthBasic
	}
	if !validAuthMethod(method) {
		return nil, ErrInvalidClient
	}

	c, err := s.repo.GetByClientID(ctx, strings.TrimSpace(in.ClientID))
	if err != nil {
		if errors.Is(err, ErrClientNotFound) {
			return nil, ErrInvalidClient
		}
		return nil, err
	}
	if c.ClientType == ClientTypePublic {
		if method != TokenEndpointAuthNone || in.ClientSecret != "" {
			return nil, ErrPublicClientSecret
		}
		return c, nil
	}
	if c.TokenEndpointAuthMethod != method {
		return nil, ErrInvalidClient
	}
	if strings.TrimSpace(in.ClientSecret) == "" {
		return nil, ErrInvalidClient
	}
	if c.ClientSecretHash == "" || !secretMatches(in.ClientSecret, c.ClientSecretHash) {
		return nil, ErrInvalidClient
	}
	return c, nil
}

// ValidatePublicTokenClient resolves an unauthenticated public client for the
// token endpoint. It is a convenience wrapper for PKCE authorization-code
// exchanges where public clients must not send a secret.
func (s *ClientService) ValidatePublicTokenClient(ctx context.Context, clientID string) (*Client, error) {
	c, err := s.AuthenticateTokenClient(ctx, ClientAuthentication{
		ClientID: clientID,
		Method:   TokenEndpointAuthNone,
	})
	if errors.Is(err, ErrPublicClientSecret) {
		return nil, ErrPublicClientSecret
	}
	return c, err
}

// ValidateRedirectURI enforces exact-string matching of the requested
// redirect_uri against the client's registered set, as required by OAuth 2.1.
func (s *ClientService) ValidateRedirectURI(c *Client, requested string) error {
	for _, uri := range c.RedirectURIs {
		if uri == requested {
			return nil
		}
	}
	return ErrRedirectURIMismatch
}

// ValidateScopes checks that every requested scope is permitted for the client
// (a subset of AllowedScopes) and returns the normalized, deduplicated, sorted
// scope list to issue. An empty result with no error is valid (the client
// requested no scopes beyond defaults).
func (s *ClientService) ValidateScopes(c *Client, requested []string) ([]string, error) {
	normalized := normalizeScopes(requested)
	allowed := map[string]struct{}{}
	for _, sc := range c.AllowedScopes {
		allowed[sc] = struct{}{}
	}
	for _, sc := range normalized {
		if _, ok := allowed[sc]; !ok {
			return nil, ErrInvalidScope
		}
	}
	return normalized, nil
}

// Per-client hygiene caps. They bound the work an admin (or future DCR client)
// can impose on a single row and keep validation deterministic.
const (
	maxRedirectURIs   = 20
	maxPostLogoutURIs = 20
	maxAllowedScopes  = 50
)

func validateClient(c *Client) error {
	if c.Name == "" {
		return ErrNameRequired
	}
	if len(c.Name) > 100 {
		return ErrNameTooLong
	}
	if c.ClientType != ClientTypePublic && c.ClientType != ClientTypeConfidential {
		return ErrInvalidClientType
	}
	if c.ClientType == ClientTypePublic && c.TokenEndpointAuthMethod != TokenEndpointAuthNone {
		return ErrPublicClientAuthMethod
	}
	if c.ClientType == ClientTypeConfidential && c.TokenEndpointAuthMethod == TokenEndpointAuthNone {
		return ErrConfidentialAuthMethod
	}
	if !validAuthMethod(c.TokenEndpointAuthMethod) {
		return ErrInvalidAuthMethod
	}
	if len(c.RedirectURIs) == 0 {
		return ErrRedirectURIRequired
	}
	if len(c.RedirectURIs) > maxRedirectURIs {
		return ErrTooManyRedirectURIs
	}
	for _, uri := range c.RedirectURIs {
		if err := validateRedirectURI(uri); err != nil {
			return err
		}
	}
	if len(c.PostLogoutRedirectURIs) > maxPostLogoutURIs {
		return ErrTooManyPostLogoutURIs
	}
	for _, uri := range c.PostLogoutRedirectURIs {
		if err := validateRedirectURI(uri); err != nil {
			return err
		}
	}
	if len(c.AllowedScopes) > maxAllowedScopes {
		return ErrTooManyScopes
	}
	for _, scope := range c.AllowedScopes {
		if !validScopeToken(scope) {
			return ErrInvalidScopeSyntax
		}
	}
	return nil
}

func validateRedirectURI(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" || u.Fragment != "" {
		return ErrInvalidRedirectURI
	}
	switch u.Scheme {
	case "https":
		return nil
	case "http":
		if isLoopbackHost(u.Hostname()) {
			return nil
		}
		return ErrInvalidRedirectURI
	default:
		return ErrInvalidRedirectURI
	}
}

func isLoopbackHost(host string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func validAuthMethod(method TokenEndpointAuthMethod) bool {
	switch method {
	case TokenEndpointAuthNone, TokenEndpointAuthBasic, TokenEndpointAuthPost:
		return true
	default:
		return false
	}
}

func validScopeToken(scope string) bool {
	if scope == "" || len(scope) > 128 {
		return false
	}
	for _, r := range scope {
		if r < 0x21 || r > 0x7e || r == '"' || r == '\\' {
			return false
		}
	}
	return true
}

func normalizeAuthMethod(clientType ClientType, method TokenEndpointAuthMethod) TokenEndpointAuthMethod {
	if clientType == ClientTypePublic {
		return TokenEndpointAuthNone
	}
	if method == "" || method == TokenEndpointAuthNone {
		return TokenEndpointAuthBasic
	}
	return method
}

func normalizeScopes(scopes []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, raw := range scopes {
		for _, scope := range strings.Fields(strings.TrimSpace(raw)) {
			if scope == "" || seen[scope] {
				continue
			}
			seen[scope] = true
			out = append(out, scope)
		}
	}
	sort.Strings(out)
	return out
}

func normalizeURIs(uris []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, raw := range uris {
		uri := strings.TrimSpace(raw)
		if uri == "" || seen[uri] {
			continue
		}
		seen[uri] = true
		out = append(out, uri)
	}
	return out
}

func randomToken(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic("oauth: rand.Read failed: " + err.Error())
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

func hashSecret(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// secretMatches reports whether the plaintext secret hashes to expectedHash,
// using a constant-time comparison so that secret verification does not leak
// timing information about the stored hash.
func secretMatches(secret, expectedHash string) bool {
	return subtle.ConstantTimeCompare([]byte(hashSecret(secret)), []byte(expectedHash)) == 1
}
