package oauth

import (
	"net"
	"net/url"
	"sort"
	"strings"
)

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
