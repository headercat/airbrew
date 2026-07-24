package oauth

import "time"

// ClientType describes whether a client can keep a secret.
type ClientType string

const (
	ClientTypePublic       ClientType = "public"
	ClientTypeConfidential ClientType = "confidential"
)

// TokenEndpointAuthMethod is the OAuth token endpoint client auth mode.
type TokenEndpointAuthMethod string

const (
	TokenEndpointAuthNone  TokenEndpointAuthMethod = "none"
	TokenEndpointAuthBasic TokenEndpointAuthMethod = "client_secret_basic"
	TokenEndpointAuthPost  TokenEndpointAuthMethod = "client_secret_post"
)

// Client is an OAuth 2.1 / OIDC client registration.
type Client struct {
	ID                      string
	ClientID                string
	Name                    string
	ClientType              ClientType
	ClientSecretHash        string
	TokenEndpointAuthMethod TokenEndpointAuthMethod
	AllowedScopes           []string
	RedirectURIs            []string
	PostLogoutRedirectURIs  []string
	IsFirstParty            bool
	RequireConsent          bool
	IsActive                bool
	CreatedAt               time.Time
	UpdatedAt               time.Time
}
