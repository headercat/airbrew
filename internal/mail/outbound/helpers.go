package outbound

import (
	"encoding/base64"
	"io"
	"net/http"
)

// b64 returns the standard base64 encoding of data.
func b64(data []byte) string { return base64.StdEncoding.EncodeToString(data) }

// readBodySnippet returns a short excerpt of an HTTP response body for error
// messages, capped so providers that return large error payloads don't blow up
// logs.
func readBodySnippet(resp *http.Response) string {
	if resp == nil || resp.Body == nil {
		return ""
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 512))
	if err != nil {
		return ""
	}
	return string(b)
}
