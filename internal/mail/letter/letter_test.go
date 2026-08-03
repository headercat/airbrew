package letter

import (
	"bytes"
	"encoding/base64"
	"strings"
	"testing"
)

// encodeBase64 is a tiny helper for building base64 attachment fixtures.
func encodeBase64(b []byte) string {
	return base64.StdEncoding.EncodeToString(b)
}

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

// TestDecodeHeaderTranscodesLegacyCharset verifies RFC2047 encoded-words
// carrying a legacy charset (EUC-KR here) decode into UTF-8 rather than being
// returned raw. This is the dominant case for Korean inbound subject lines.
func TestDecodeHeaderTranscodesLegacyCharset(t *testing.T) {
	got := decodeHeader("=?EUC-KR?B?vsiz5w==?=")
	if got != "안녕" {
		t.Fatalf("got %q, want 안녕", got)
	}
	// Subject with a leading "Re: " keeps the ASCII prefix untouched.
	if got := decodeHeader("Re: =?EUC-KR?B?vsiz5w==?="); got != "Re: 안녕" {
		t.Fatalf("got %q, want \"Re: 안녕\"", got)
	}
}

// TestParseSynthesisesTextFromHTMLOnly ensures an HTML-only message still// yields a non-empty plain-text body so search and the SPA list snippet have
// something to show.
func TestParseSynthesisesTextFromHTMLOnly(t *testing.T) {
	raw := strings.Join([]string{
		"From: bob@example.com",
		"To: alice@example.com",
		"Subject: html only",
		"Message-ID: <html1@example.com>",
		"Content-Type: text/html; charset=utf-8",
		"",
		"<html><body><p>Hello <b>world</b></p><p>Second line</p></body></html>",
	}, "\r\n")
	msg, err := Parse([]byte(raw))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !strings.Contains(msg.Text, "Hello world") || !strings.Contains(msg.Text, "Second line") {
		t.Fatalf("text = %q, want synthesised plain text containing both paragraphs", msg.Text)
	}
	if msg.HTML == "" {
		t.Fatalf("html body should be preserved")
	}
}

// TestBuildRFC822EncodesNonASCIIDisplayName checks that a Korean display
// name on the From/To header is RFC2047-encoded so strict SMTP relays and
// SES (no SMTPUTF8) accept the message instead of rejecting or mangling it.
func TestBuildRFC822EncodesNonASCIIDisplayName(t *testing.T) {
	out, err := BuildRFC822(Outgoing{
		From:    Address{Name: "홍길동", Address: "alice@example.com"},
		To:      []Address{{Name: "김철수", Address: "bob@example.com"}},
		Subject: "hello",
		Text:    "body",
	})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	s := string(out)
	if !strings.Contains(s, "From: =?utf-8?") {
		t.Fatalf("From header not RFC2047-encoded:\n%s", s)
	}
	if !strings.Contains(s, "To: =?utf-8?") {
		t.Fatalf("To header not RFC2047-encoded:\n%s", s)
	}
	if strings.Contains(s, "홍길동 <alice@example.com>") {
		t.Fatalf("raw non-ASCII From leaked:\n%s", s)
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

// TestParsePreservesBinaryAttachmentBytes guards against charset transcoding
// being applied to non-text parts. A PDF whose producer also set charset=utf-8
// must be returned byte-for-byte; transcoding it would corrupt the download.
func TestParsePreservesBinaryAttachmentBytes(t *testing.T) {
	body := []byte("%PDF-1.4\n%\xe2\xe3\xcf\xd3\n%binary\x00\x01\x02 payload")
	raw := strings.Join([]string{
		"From: bob@example.com",
		"To: alice@example.com",
		"Subject: binary",
		"Message-ID: <bin1@example.com>",
		"MIME-Version: 1.0",
		"Content-Type: multipart/mixed; boundary=\"EDGE\"",
		"",
		"--EDGE",
		"Content-Type: text/plain; charset=utf-8",
		"",
		"see attached",
		"--EDGE",
		"Content-Type: application/pdf; charset=utf-8; name=\"doc.pdf\"",
		"Content-Transfer-Encoding: base64",
		"Content-Disposition: attachment; filename=\"doc.pdf\"",
		"",
		encodeBase64(body),
		"--EDGE--",
	}, "\r\n")
	msg, err := Parse([]byte(raw))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(msg.Attachments) != 1 {
		t.Fatalf("attachments = %d, want 1", len(msg.Attachments))
	}
	got := msg.Attachments[0].Data
	if !bytes.Equal(got, body) {
		t.Fatalf("binary attachment was mutated:\nwant %x\n got %x", body, got)
	}
}
