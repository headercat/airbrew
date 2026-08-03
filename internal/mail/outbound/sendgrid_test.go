package outbound

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/headercat/airbrew/internal/mail/letter"
)

func TestSendgridIncludesAttachments(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()

	drv, err := buildSendgrid([]byte(`{"api_key":"test","endpoint":"` + srv.URL + `"}`))
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	err = drv.Send(context.Background(), letter.Outgoing{
		From:    letter.Address{Address: "alice@example.com"},
		To:      []letter.Address{{Address: "bob@example.com"}},
		Subject: "hello",
		Text:    "body",
		Attachments: []letter.Attachment{{
			Filename:    "notes.txt",
			ContentType: "text/plain",
			Data:        []byte("ship it"),
		}},
	})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	atts, ok := body["attachments"].([]any)
	if !ok || len(atts) != 1 {
		t.Fatalf("attachments = %#v, want one attachment", body["attachments"])
	}
	first, ok := atts[0].(map[string]any)
	if !ok {
		t.Fatalf("attachment shape = %#v", atts[0])
	}
	if first["filename"] != "notes.txt" || first["content"] != "c2hpcCBpdA==" {
		t.Fatalf("attachment = %#v", first)
	}
}

// TestCloudflareRejectsPlaintextURL verifies the driver refuses to ship the
// bearer secret and mail body over a non-https transport unless the operator
// explicitly opts in via allow_insecure.
func TestCloudflareRejectsPlaintextURL(t *testing.T) {
	cases := []struct {
		name    string
		cfg     string
		wantErr string
	}{
		{"plain http rejected", `{"worker_url":"http://send.example/","secret":"x"}`, "https"},
		{"ftp rejected", `{"worker_url":"ftp://send.example/"}`, "scheme"},
		{"https ok", `{"worker_url":"https://send.example/"}`, ""},
		{"http allowed with flag", `{"worker_url":"http://localhost/","allow_insecure":true}`, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := buildCloudflare([]byte(tc.cfg))
			switch {
			case tc.wantErr == "" && err != nil:
				t.Fatalf("expected ok, got %v", err)
			case tc.wantErr != "" && err == nil:
				t.Fatalf("expected error containing %q, got nil", tc.wantErr)
			case tc.wantErr != "" && err != nil && !strings.Contains(err.Error(), tc.wantErr):
				t.Fatalf("error %q does not contain %q", err.Error(), tc.wantErr)
			}
		})
	}
}
