package letter

import (
	"strings"
	"testing"
)

func TestParseQuotedPrintableBody(t *testing.T) {
	raw := strings.Join([]string{
		"From: bob@example.com",
		"To: alice@example.com",
		"Subject: qp",
		"Message-ID: <qp1@example.com>",
		"Content-Type: text/plain; charset=utf-8",
		"Content-Transfer-Encoding: quoted-printable",
		"",
		"Hello=2C=",
		" world=21",
	}, "\r\n")
	msg, err := Parse([]byte(raw))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if msg.Text != "Hello, world!" {
		t.Fatalf("text = %q, want decoded quoted-printable body", msg.Text)
	}
}
