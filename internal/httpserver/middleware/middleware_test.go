package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"log/slog"
)

// TestAccessLogPassesThroughFlusher guards the SSE streaming path: the
// statusWriter wrapper used by AccessLog must still satisfy http.Flusher so
// that chat/AI SSE handlers downstream of the middleware chain can flush
// frames incrementally. Without the Flush/Unwrap methods the handler's
// `w.(http.Flusher)` assertion fails and it returns 500 streaming_unsupported.
func TestAccessLogPassesThroughFlusher(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, `{"error":"streaming_unsupported"}`, http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("data: hi\n\n"))
		f.Flush()
	})

	// Mirror the production chain used by httpserver.New.
	chain := Chain(handler,
		SecurityHeaders(false),
		CSRF,
		RequestID,
		Recover(slog.Default()),
		AccessLog(slog.Default()),
	)
	srv := httptest.NewServer(chain)
	defer srv.Close()

	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (Flusher must pass through AccessLog)", resp.StatusCode)
	}
	if !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("content-type = %q, want text/event-stream", resp.Header.Get("Content-Type"))
	}
}
