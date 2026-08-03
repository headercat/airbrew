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

// TestParseTranscodesLegacyCharset checks that an EUC-KR encoded body is
// transcoded to UTF-8 so the SPA does not render mojibake for legacy Korean
// mail. The same path covers ISO-2022-JP and Windows-1252.
func TestParseTranscodesLegacyCharset(t *testing.T) {
	// "안녕" in EUC-KR (안=BE C8, 녕=B3 E7), QP-encoded.
	raw := strings.Join([]string{
		"From: bob@example.com",
		"To: alice@example.com",
		"Subject: legacy",
		"Message-ID: <legacy1@example.com>",
		"Content-Type: text/plain; charset=euc-kr",
		"Content-Transfer-Encoding: quoted-printable",
		"",
		"=BE=C8=B3=E7",
	}, "\r\n")
	msg, err := Parse([]byte(raw))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if msg.Text != "안녕" {
		t.Fatalf("text = %q, want 안녕", msg.Text)
	}
}

// TestBuildRFC822EncodesNonASCIIFilename checks that non-ASCII attachment
// filenames are emitted with RFC 2231 encoding (filename*=UTF-8''…) so
// strict clients like Outlook do not rename or drop the attachment.
func TestBuildRFC822EncodesNonASCIIFilename(t *testing.T) {
	out, err := BuildRFC822(Outgoing{
		From:    Address{Address: "alice@example.com"},
		To:      []Address{{Address: "bob@example.com"}},
		Subject: "files",
		Text:    "see attached",
		Attachments: []Attachment{{
			Filename:    "보고서.pdf",
			ContentType: "application/pdf",
			Data:        []byte("%PDF-1.4"),
		}},
	})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	s := string(out)
	if !strings.Contains(s, "utf-8''") {
		t.Fatalf("expected RFC 2231 filename* encoding for non-ASCII name, got:\n%s", s)
	}
	if strings.Contains(s, "filename=\"보고서.pdf\"") || strings.Contains(s, "filename=보고서.pdf") {
		t.Fatalf("raw non-ASCII filename should not be present:\n%s", s)
	}
}
