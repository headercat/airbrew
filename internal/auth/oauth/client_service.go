package oauth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
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

func validateClient(c *Client) error {
	if c.Name == "" {
		return errors.New("name required")
	}
	if len(c.Name) > 100 {
		return errors.New("name must be 100 characters or fewer")
	}
	if c.ClientType != ClientTypePublic && c.ClientType != ClientTypeConfidential {
		return errors.New("client_type must be public or confidential")
	}
	if c.ClientType == ClientTypePublic && c.TokenEndpointAuthMethod != TokenEndpointAuthNone {
		return errors.New("public clients must use token_endpoint_auth_method none")
	}
	if c.ClientType == ClientTypeConfidential && c.TokenEndpointAuthMethod == TokenEndpointAuthNone {
		return errors.New("confidential clients must use a client secret auth method")
	}
	if len(c.RedirectURIs) == 0 {
		return errors.New("at least one redirect_uri is required")
	}
	for _, uri := range c.RedirectURIs {
		if err := validateRedirectURI(uri); err != nil {
			return err
		}
	}
	for _, uri := range c.PostLogoutRedirectURIs {
		if err := validateRedirectURI(uri); err != nil {
			return err
		}
	}
	return nil
}

func validateRedirectURI(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" || u.Fragment != "" {
		return errors.New("redirect URIs must be absolute http(s) URLs without fragments")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return errors.New("redirect URIs must use http or https")
	}
	return nil
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
