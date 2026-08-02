package outbound

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
