// Package provider — upstream_error.go
//
// upstreamError carries a non-2xx provider response without leaking its
// body through Error(). The body is often verbose and may echo back part
// of an API key (e.g. "Incorrect API key provided: sk-proj-***"), so we
// keep it for logging and surface only the HTTP status to callers.
package provider

import "fmt"

// upstreamError is returned by drivers when the provider responds with a
// non-2xx status. Body holds the raw response for diagnostic logging;
// Error() omits it.
type upstreamError struct {
	status int
	body   string
}

func (e *upstreamError) Error() string {
	return fmt.Sprintf("provider: upstream error: http %d", e.status)
}

// Is makes errors.Is(err, ErrUpstream) true for any *upstreamError so
// callers can branch on the sentinel without importing this type.
func (e *upstreamError) Is(target error) bool {
	return target == ErrUpstream
}

// Status returns the upstream HTTP status code.
func (e *upstreamError) Status() int { return e.status }

// Body returns the raw upstream response body. Drivers populate it from
// the bounded read in httpError(); handlers may log it but must never
// forward it to the client verbatim.
func (e *upstreamError) Body() string { return e.body }

// AsUpstream returns the *upstreamError inside err (if any) so the
// runtime / handler can log its Body without leaking it via Error().
func AsUpstream(err error) *upstreamError {
	if err == nil {
		return nil
	}
	type causer interface{ Unwrap() error }
	if u, ok := err.(*upstreamError); ok {
		return u
	}
	if c, ok := err.(causer); ok {
		return AsUpstream(c.Unwrap())
	}
	return nil
}
