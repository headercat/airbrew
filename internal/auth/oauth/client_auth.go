package oauth

import (
	"net/http"
	"strings"
)

// ClientAuthenticationFromRequest extracts OAuth token endpoint client
// credentials from HTTP Basic auth or form fields. It rejects requests that use
// both mechanisms at once, because RFC 6749 allows only one authentication
// method per request.
func ClientAuthenticationFromRequest(r *http.Request) (ClientAuthentication, error) {
	basicID, basicSecret, hasBasic := r.BasicAuth()
	if err := r.ParseForm(); err != nil {
		return ClientAuthentication{}, err
	}

	formID := strings.TrimSpace(r.PostForm.Get("client_id"))
	formSecret := r.PostForm.Get("client_secret")
	hasFormSecret := formID != "" || formSecret != ""
	if hasBasic && hasFormSecret {
		return ClientAuthentication{}, ErrInvalidClient
	}
	if hasBasic {
		return ClientAuthentication{
			ClientID:     basicID,
			ClientSecret: basicSecret,
			Method:       TokenEndpointAuthBasic,
		}, nil
	}
	if formSecret != "" {
		return ClientAuthentication{
			ClientID:     formID,
			ClientSecret: formSecret,
			Method:       TokenEndpointAuthPost,
		}, nil
	}
	return ClientAuthentication{
		ClientID: formID,
		Method:   TokenEndpointAuthNone,
	}, nil
}
