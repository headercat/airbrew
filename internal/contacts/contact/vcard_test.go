package contact

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestWriteVCardEmitsIdentityAndGroups(t *testing.T) {
	bday := time.Date(1990, 5, 3, 0, 0, 0, 0, time.UTC)
	updated := time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC)
	c := &Contact{
		ID:          "cid",
		UID:         "uid-external",
		DisplayName: "Ada Lovelace",
		GivenName:   "Ada",
		FamilyName:  "Lovelace",
		Emails:      []Email{{Value: "ada@example.com", Type: "work"}},
		Birthday:    &bday,
		UpdatedAt:   updated,
	}
	var buf bytes.Buffer
	if err := WriteVCard(&buf, c, []string{"Work", "Family"}); err != nil {
		t.Fatalf("WriteVCard: %v", err)
	}
	out := buf.String()
	for _, want := range []string{
		"BEGIN:VCARD",
		"VERSION:4.0",
		"UID:uid-external",
		"REV:20240102T030405Z",
		"FN:Ada Lovelace",
		"N:Lovelace;Ada;;;",
		"BDAY:1990-05-03",
		"CATEGORIES:Work,Family",
		"END:VCARD",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in output:\n%s", want, out)
		}
	}
}

func TestVCardRoundTripPreservesFields(t *testing.T) {
	bday := time.Date(1990, 5, 3, 0, 0, 0, 0, time.UTC)
	c := &Contact{
		UID:         "round-trip-uid",
		NamePrefix:  "Dr",
		GivenName:   "Ada",
		MiddleName:  "Augusta",
		FamilyName:  "Lovelace",
		DisplayName: "Ada Lovelace",
		Nickname:    "Ada",
		Company:     "Analytical Engine Co",
		Title:       "Mathematician",
		Department:  "Research",
		Emails:      []Email{{Value: "ada@example.com", Type: "work"}},
		Phones:      []Phone{{Value: "+1 555 1234", Type: "mobile"}},
		Addresses: []Address{{
			Type: "home", Street: "1 Main St", Locality: "London",
			Region: "UK", PostalCode: "W1", Country: "England",
		}},
		URLs:     []URL{{Value: "https://example.com", Type: "home"}},
		Birthday: &bday,
		Notes:    "first programmer",
	}
	var buf bytes.Buffer
	if err := WriteVCard(&buf, c, []string{"Work"}); err != nil {
		t.Fatalf("WriteVCard: %v", err)
	}
	inputs, err := ParseVCards(&buf)
	if err != nil {
		t.Fatalf("ParseVCards: %v", err)
	}
	if len(inputs) != 1 {
		t.Fatalf("got %d cards, want 1", len(inputs))
	}
	in := inputs[0]
	checks := []struct{ name, got, want string }{
		{"UID", in.UID, c.UID},
		{"NamePrefix", in.NamePrefix, c.NamePrefix},
		{"GivenName", in.GivenName, c.GivenName},
		{"MiddleName", in.MiddleName, c.MiddleName},
		{"FamilyName", in.FamilyName, c.FamilyName},
		{"DisplayName", in.DisplayName, c.DisplayName},
		{"Nickname", in.Nickname, c.Nickname},
		{"Company", in.Company, c.Company},
		{"Title", in.Title, c.Title},
		{"Department", in.Department, c.Department},
		{"Notes", in.Notes, c.Notes},
	}
	for _, ch := range checks {
		if ch.got != ch.want {
			t.Errorf("%s: got %q, want %q", ch.name, ch.got, ch.want)
		}
	}
	if len(in.Emails) != 1 || in.Emails[0].Value != "ada@example.com" {
		t.Errorf("emails round-trip mismatch: %#v", in.Emails)
	}
	if len(in.Phones) != 1 || in.Phones[0].Value != "+1 555 1234" {
		t.Errorf("phones round-trip mismatch: %#v", in.Phones)
	}
	if len(in.Addresses) != 1 || in.Addresses[0].Street != "1 Main St" {
		t.Errorf("addresses round-trip mismatch: %#v", in.Addresses)
	}
	if len(in.URLs) != 1 || in.URLs[0].Value != "https://example.com" {
		t.Errorf("urls round-trip mismatch: %#v", in.URLs)
	}
	if in.Birthday == nil || !in.Birthday.Equal(bday) {
		t.Errorf("birthday round-trip mismatch: %v", in.Birthday)
	}
	if len(in.GroupNames) != 1 || in.GroupNames[0] != "Work" {
		t.Errorf("group names round-trip mismatch: %#v", in.GroupNames)
	}
}

func TestParseVCardsHandlesLineFoldingAndMultipleCards(t *testing.T) {
	in := "BEGIN:VCARD\r\nVERSION:4.0\r\nFN:Jane\r\nN:;;;\r\nEMAIL:with folded\r\n continued-line\r\nEND:VCARD\r\nBEGIN:VCARD\r\nVERSION:4.0\r\nFN:John\r\nN:;;;\r\nEND:VCARD\r\n"
	inputs, err := ParseVCards(strings.NewReader(in))
	if err != nil {
		t.Fatalf("ParseVCards: %v", err)
	}
	if len(inputs) != 2 {
		t.Fatalf("got %d cards, want 2", len(inputs))
	}
	if inputs[0].DisplayName != "Jane" {
		t.Errorf("first card FN: got %q, want Jane", inputs[0].DisplayName)
	}
	if inputs[0].Emails[0].Value != "with foldedcontinued-line" {
		t.Errorf("folded line not joined: %q", inputs[0].Emails[0].Value)
	}
	if inputs[1].DisplayName != "John" {
		t.Errorf("second card FN: got %q, want John", inputs[1].DisplayName)
	}
}

func TestParseVCardsStripsAppleGroupPrefix(t *testing.T) {
	// Apple Contacts (macOS/iOS) emits properties with a group prefix like
	// "item1.TEL"; these must be parsed as TEL, not dropped as unknown.
	in := "BEGIN:VCARD\r\nVERSION:3.0\r\nFN:Jane\r\nN:;;;\r\n" +
		"item1.TEL;TYPE=cell:+15551234\r\n" +
		"item1.EMAIL;TYPE=home:jane@example.com\r\n" +
		"item2.ADR;TYPE=work:;;2 Main St;London;;;;\r\n" +
		"END:VCARD\r\n"
	inputs, err := ParseVCards(strings.NewReader(in))
	if err != nil {
		t.Fatalf("ParseVCards: %v", err)
	}
	if len(inputs) != 1 {
		t.Fatalf("got %d cards, want 1", len(inputs))
	}
	c := inputs[0]
	if len(c.Phones) != 1 || c.Phones[0].Value != "+15551234" {
		t.Errorf("grouped TEL lost: %#v", c.Phones)
	}
	if len(c.Emails) != 1 || c.Emails[0].Value != "jane@example.com" {
		t.Errorf("grouped EMAIL lost: %#v", c.Emails)
	}
	if len(c.Addresses) != 1 || c.Addresses[0].Street != "2 Main St" {
		t.Errorf("grouped ADR lost: %#v", c.Addresses)
	}
}

func TestParseCategoriesHandlesEscapedComma(t *testing.T) {
	in := "BEGIN:VCARD\r\nVERSION:4.0\r\nFN:Pat\r\nN:;;;\r\n" +
		"CATEGORIES:Foo\\,Bar,Baz\r\nEND:VCARD\r\n"
	inputs, err := ParseVCards(strings.NewReader(in))
	if err != nil {
		t.Fatalf("ParseVCards: %v", err)
	}
	if len(inputs) != 1 || len(inputs[0].GroupNames) != 2 {
		t.Fatalf("expected 2 categories, got %#v", inputs[0].GroupNames)
	}
	if inputs[0].GroupNames[0] != "Foo,Bar" || inputs[0].GroupNames[1] != "Baz" {
		t.Errorf("escaped comma split wrong: %#v", inputs[0].GroupNames)
	}
}

func TestEscapeAndUnescape(t *testing.T) {
	cases := []string{"plain", "with,comma", "with;semi", `back\slash`, "line\nbreak"}
	for _, raw := range cases {
		escaped := escape(raw)
		got := unescape(escaped)
		if got != raw {
			t.Errorf("escape/unescape(%q): escaped=%q got=%q", raw, escaped, got)
		}
	}
}
