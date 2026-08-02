// Package handler — sse.go
//
// Helpers for writing Server-Sent Events: writeEvent frames one event,
// and writeKeepAlive flushes a comment line so proxies with a long
// buffer threshold do not kill the stream while a tool is dispatching.
package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// SSEWriter wraps an http.ResponseWriter with a manual flusher. SSE is
// intentionally low-level; we want control over flushing per token.
type SSEWriter struct {
	w       http.ResponseWriter
	flusher http.Flusher
}

// NewSSEWriter upgrades w. If the ResponseWriter does not implement
// http.Flusher, writes still work but may be buffered — the caller
// should treat that as an unsupported transport.
func NewSSEWriter(w http.ResponseWriter) (*SSEWriter, bool) {
	f, ok := w.(http.Flusher)
	if !ok {
		return nil, false
	}
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache, no-transform")
	h.Set("Connection", "keep-alive")
	h.Set("X-Accel-Buffering", "no") // disable nginx buffering
	w.WriteHeader(http.StatusOK)
	f.Flush()
	return &SSEWriter{w: w, flusher: f}, true
}

// Event writes one SSE event frame of the form:
//
//	event: <name>\n
//	data: <json>\n\n
//
// The JSON payload is encoded once; the resulting line is written
// verbatim so multi-line JSON does not break SSE framing (we encode and
// rely on JSON never producing raw newlines).
func (s *SSEWriter) Event(name string, payload any) error {
	b, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(s.w, "event: %s\ndata: %s\n\n", name, b); err != nil {
		return err
	}
	s.flusher.Flush()
	return nil
}

// KeepAlive writes a comment frame. Call from a ticker when the runtime
// may be silent for >15s (e.g. a long tool call) so the client knows the
// connection is live.
func (s *SSEWriter) KeepAlive() {
	fmt.Fprint(s.w, ": keep-alive\n\n")
	s.flusher.Flush()
}

// Heartbeat spawns a goroutine that writes a keep-alive every interval
// until the returned stop function is called. It is the caller's
// responsibility to stop before the SSEWriter is torn down.
func (s *SSEWriter) Heartbeat(interval time.Duration) (stop func()) {
	t := time.NewTicker(interval)
	done := make(chan struct{})
	go func() {
		for {
			select {
			case <-done:
				t.Stop()
				return
			case <-t.C:
				s.KeepAlive()
			}
		}
	}()
	return func() { close(done) }
}
