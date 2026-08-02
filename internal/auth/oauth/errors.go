package oauth

import "errors"

// Client lookup / persistence errors. Handlers map these to HTTP status codes
// (see oauthClientError in the admin handler).
var (
	// ErrClientNotFound is returned when no active client matches a lookup.
	ErrClientNotFound = errors.New("oauth client not found")
	// ErrClientIDTaken is returned when a generated or supplied client_id
	// collides with an existing row.
	ErrClientIDTaken = errors.New("oauth client_id already exists")
)

// Validation errors returned by ClientService.Create / Update.
var (
	ErrNameRequired           = errors.New("name required")
	ErrNameTooLong            = errors.New("name must be 100 characters or fewer")
	ErrInvalidClientType      = errors.New("client_type must be public or confidential")
	ErrPublicClientAuthMethod = errors.New("public clients must use token_endpoint_auth_method none")
	ErrConfidentialAuthMethod = errors.New("confidential clients must use a client secret auth method")
	ErrRedirectURIRequired    = errors.New("at least one redirect_uri is required")
	ErrTooManyRedirectURIs    = errors.New("too many redirect_uris")
	ErrTooManyPostLogoutURIs  = errors.New("too many post_logout_redirect_uris")
	ErrInvalidRedirectURI     = errors.New("invalid redirect_uri")
	ErrInvalidAuthMethod      = errors.New("invalid token_endpoint_auth_method")
	ErrInvalidScopeSyntax     = errors.New("invalid scope syntax")
	ErrTooManyScopes          = errors.New("too many allowed scopes")
)

// Protocol errors returned by the helpers that the authorize / token endpoints
// (milestones 3-5) will call.
var (
	// ErrInvalidClient is returned when a client_id is unknown, inactive, or a
	// presented secret does not match. It deliberately conflates the cases so
	// the token endpoint cannot be used to enumerate registered client_ids.
	ErrInvalidClient = errors.New("invalid_client")
	// ErrPublicClientSecret is returned when a secret is presented for a public
	// client (which cannot hold one), or when rotating a public client.
	ErrPublicClientSecret = errors.New("public clients do not present a client secret")
	// ErrRedirectURIMismatch is returned by ValidateRedirectURI when the
	// requested redirect_uri is not registered.
	ErrRedirectURIMismatch = errors.New("redirect_uri mismatch")
	// ErrInvalidScope is returned by ValidateScopes when a requested scope is
	// not permitted for the client.
	ErrInvalidScope = errors.New("invalid_scope")
)
