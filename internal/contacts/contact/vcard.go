package contact

import (
	"bufio"
	"io"
	"strings"
	"time"
)

// vCard 4.0 import/export helpers (RFC 6350). The parser is deliberately
// lenient: it accepts 2.1/3.0/4.0 single- and multi-card streams, folds
// continued lines, and ignores unknown properties. The serializer always
// emits 4.0.

// maxVCards caps an import so a hostile upload cannot exhaust memory.
const maxVCards = 5000

// ParseVCards reads one or more vCard records from r and returns the parsed
// contacts. It does not assign IDs or user ownership — the caller does.
func ParseVCards(r io.Reader) ([]CreateContactInput, error) {
	br := bufio.NewReader(r)
	raw, err := unfold(br)
	if err != nil {
		return nil, err
	}
	var out []CreateContactInput
	for _, card := range splitCards(raw) {
		if len(card) == 0 {
			continue
		}
		c, ok := parseOneCard(card)
		if !ok {
			continue
		}
		out = append(out, c)
		if len(out) >= maxVCards {
			break
		}
	}
	return out, nil
}

// unfold reads the whole stream, joining physical lines that were folded (a
// continuation line begins with a space or tab) into logical lines, and
// normalising CRLF/CR to LF.
func unfold(r *bufio.Reader) ([]string, error) {
	var lines []string
	for {
		line, err := r.ReadString('\n')
		if err != nil && err != io.EOF {
			return nil, err
		}
		if line == "" && err == io.EOF {
			break
		}
		line = strings.TrimRight(line, "\r\n")
		if len(line) > 0 && (line[0] == ' ' || line[0] == '\t') {
			if len(lines) > 0 {
				lines[len(lines)-1] += line[1:]
			}
		} else {
			lines = append(lines, line)
		}
		if err == io.EOF {
			break
		}
	}
	return lines, nil
}

// splitCards partitions logical lines into per-card slices, delimited by
// BEGIN:VCARD ... END:VCARD (case-insensitive).
func splitCards(lines []string) [][]string {
	var cards [][]string
	var cur []string
	in := false
	for _, l := range lines {
		upper := strings.ToUpper(l)
		if strings.HasPrefix(upper, "BEGIN:VCARD") {
			in = true
			cur = []string{}
			continue
		}
		if strings.HasPrefix(upper, "END:VCARD") {
			if in {
				cards = append(cards, cur)
			}
			in = false
			cur = nil
			continue
		}
		if in {
			cur = append(cur, l)
		}
	}
	return cards
}

// parseOneCard parses a single card's property lines into a CreateContactInput.
// Returns ok=false for a card with no usable data.
func parseOneCard(lines []string) (CreateContactInput, bool) {
	var c CreateContactInput
	hasData := false
	for _, l := range lines {
		name, params, value := splitProperty(l)
		if name == "" {
			continue
		}
		switch name {
		case "FN":
			c.DisplayName = unescape(value)
			hasData = true
		case "N":
			parts := splitSemicolons(value)
			safe := func(i int) string {
				if i < len(parts) {
					return unescape(parts[i])
				}
				return ""
			}
			c.FamilyName = safe(0)
			c.GivenName = safe(1)
			c.MiddleName = safe(2)
			c.NamePrefix = safe(3)
			c.NameSuffix = safe(4)
			hasData = true
		case "NICKNAME":
			for _, n := range strings.Split(value, ",") {
				n = strings.TrimSpace(unescape(n))
				if n != "" {
					c.Nickname = n
					break
				}
			}
		case "ORG":
			parts := splitSemicolons(value)
			if len(parts) > 0 {
				c.Company = unescape(parts[0])
			}
			if len(parts) > 1 {
				c.Department = unescape(parts[1])
			}
			hasData = true
		case "TITLE":
			c.Title = unescape(value)
		case "EMAIL":
			if v := strings.TrimSpace(value); v != "" {
				c.Emails = append(c.Emails, Email{Value: unescape(v), Type: typeParam(params)})
				hasData = true
			}
		case "TEL":
			if v := strings.TrimSpace(value); v != "" {
				c.Phones = append(c.Phones, Phone{Value: unescape(v), Type: typeParam(params)})
				hasData = true
			}
		case "URL":
			if v := strings.TrimSpace(value); v != "" {
				c.URLs = append(c.URLs, URL{Value: unescape(v), Type: typeParam(params)})
			}
		case "IMPP":
			if v := strings.TrimSpace(value); v != "" {
				c.IMs = append(c.IMs, IM{Value: unescape(v), Type: typeParam(params)})
			}
		case "ADR":
			a := parseADR(value, typeParam(params))
			if a != (Address{}) {
				c.Addresses = append(c.Addresses, a)
			}
		case "NOTE":
			if c.Notes == "" {
				c.Notes = unescape(value)
			} else {
				c.Notes += "\n" + unescape(value)
			}
		case "BDAY":
			if t, ok := parseBirthday(value); ok {
				c.Birthday = &t
			}
		case "UID":
			c.UID = unescape(value)
		case "CATEGORIES":
			for _, n := range strings.Split(value, ",") {
				if n = strings.TrimSpace(unescape(n)); n != "" {
					c.GroupNames = append(c.GroupNames, n)
				}
			}
		}
	}
	if c.DisplayName == "" {
		c.DisplayName = joinNames(c.GivenName, c.FamilyName)
	}
	return c, hasData
}

// splitProperty splits "NAME;PARAM=...:VALUE" into its parts. NAME is
// upper-cased. Unrecognised / malformed lines yield ("", nil, "").
func splitProperty(line string) (name string, params map[string]string, value string) {
	idx := strings.Index(line, ":")
	if idx < 0 {
		return "", nil, ""
	}
	left := line[:idx]
	value = line[idx+1:]
	params = map[string]string{}
	parts := strings.Split(left, ";")
	name = strings.ToUpper(strings.TrimSpace(parts[0]))
	for _, p := range parts[1:] {
		if eq := strings.Index(p, "="); eq >= 0 {
			k := strings.ToUpper(strings.TrimSpace(p[:eq]))
			v := strings.TrimSpace(p[eq+1:])
			params[k] = v
		}
	}
	return name, params, value
}

// typeParam pulls the first type out of a TYPE= param (case-insensitive).
// Returns "" when absent.
func typeParam(params map[string]string) Type {
	if v, ok := params["TYPE"]; ok {
		if c := strings.Split(v, ","); len(c) > 0 {
			return Type(strings.ToLower(strings.TrimSpace(c[0])))
		}
	}
	return ""
}

// splitSemicolons splits a value on unescaped semicolons.
func splitSemicolons(s string) []string {
	var out []string
	var cur strings.Builder
	for i := 0; i < len(s); i++ {
		ch := s[i]
		if ch == '\\' && i+1 < len(s) {
			cur.WriteByte(s[i+1])
			i++
			continue
		}
		if ch == ';' {
			out = append(out, cur.String())
			cur.Reset()
			continue
		}
		cur.WriteByte(ch)
	}
	out = append(out, cur.String())
	return out
}

// parseADR maps the 7-semicolon ADR value to the Address struct.
// Order: POBox ; Extended ; Street ; Locality ; Region ; PostalCode ; Country
func parseADR(value string, t Type) Address {
	parts := splitSemicolons(value)
	safe := func(i int) string {
		if i < len(parts) {
			return unescape(parts[i])
		}
		return ""
	}
	return Address{
		Type:       t,
		Street:     safe(2),
		Locality:   safe(3),
		Region:     safe(4),
		PostalCode: safe(5),
		Country:    safe(6),
	}
}

// parseBirthday accepts YYYY-MM-DD, YYYY-MM-DDTHH:MM:SS[Z|±HH:MM], and
// --MM-DD (partial). Returns the zero time + false when unreadable.
func parseBirthday(value string) (time.Time, bool) {
	v := strings.TrimSpace(value)
	v = strings.TrimPrefix(v, "X-")
	formats := []string{
		"2006-01-02",
		"20060102",
		time.RFC3339,
		"2006-01-02T15:04:05",
	}
	for _, f := range formats {
		if t, err := time.Parse(f, v); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// unescape reverses vCard backslash escaping (\n, \,, \;, \\).
func unescape(s string) string {
	if !strings.ContainsRune(s, '\\') {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		ch := s[i]
		if ch == '\\' && i+1 < len(s) {
			switch s[i+1] {
			case 'n', 'N':
				b.WriteByte('\n')
			case ',':
				b.WriteByte(',')
			case ';':
				b.WriteByte(';')
			case '\\':
				b.WriteByte('\\')
			default:
				b.WriteByte(s[i+1])
			}
			i++
			continue
		}
		b.WriteByte(ch)
	}
	return b.String()
}

// --- serialization ---------------------------------------------------------

// WriteVCard serialises a contact as a vCard 4.0 record to w. groupNames (when
// non-empty) are emitted as CATEGORIES so group membership survives a
// round-trip; the contact's stable id and last-updated timestamp are emitted
// as UID and REV respectively so re-import can match existing records.
func WriteVCard(w io.Writer, c *Contact, groupNames []string) error {
	var b strings.Builder
	b.WriteString("BEGIN:VCARD\r\n")
	b.WriteString("VERSION:4.0\r\n")
	if uid := c.UID; uid != "" {
		b.WriteString("UID:" + escape(uid) + "\r\n")
	} else if c.ID != "" {
		b.WriteString("UID:" + escape(c.ID) + "\r\n")
	}
	if !c.UpdatedAt.IsZero() {
		b.WriteString("REV:" + c.UpdatedAt.UTC().Format("20060102T150405Z") + "\r\n")
	}
	b.WriteString("FN:" + escape(c.DisplayName) + "\r\n")
	b.WriteString("N:" + escape(c.FamilyName) + ";" + escape(c.GivenName) + ";" +
		escape(c.MiddleName) + ";" + escape(c.NamePrefix) + ";" + escape(c.NameSuffix) + "\r\n")
	if c.Nickname != "" {
		b.WriteString("NICKNAME:" + escape(c.Nickname) + "\r\n")
	}
	if c.Company != "" || c.Department != "" {
		b.WriteString("ORG:" + escape(c.Company) + ";" + escape(c.Department) + "\r\n")
	}
	if c.Title != "" {
		b.WriteString("TITLE:" + escape(c.Title) + "\r\n")
	}
	for _, e := range c.Emails {
		prop := "EMAIL"
		if e.Type != "" {
			prop += ";TYPE=" + string(e.Type)
		}
		b.WriteString(prop + ":" + escape(e.Value) + "\r\n")
	}
	for _, p := range c.Phones {
		prop := "TEL"
		if p.Type != "" {
			prop += ";TYPE=" + string(p.Type)
		}
		b.WriteString(prop + ":" + escape(p.Value) + "\r\n")
	}
	for _, u := range c.URLs {
		prop := "URL"
		if u.Type != "" {
			prop += ";TYPE=" + string(u.Type)
		}
		b.WriteString(prop + ":" + escape(u.Value) + "\r\n")
	}
	for _, m := range c.IMs {
		prop := "IMPP"
		if m.Type != "" {
			prop += ";TYPE=" + string(m.Type)
		}
		b.WriteString(prop + ":" + escape(m.Value) + "\r\n")
	}
	for _, a := range c.Addresses {
		prop := "ADR"
		if a.Type != "" {
			prop += ";TYPE=" + string(a.Type)
		}
		b.WriteString(prop + ":;;" + escape(a.Street) + ";" + escape(a.Locality) + ";" +
			escape(a.Region) + ";" + escape(a.PostalCode) + ";" + escape(a.Country) + "\r\n")
	}
	if c.Birthday != nil {
		b.WriteString("BDAY:" + c.Birthday.UTC().Format("2006-01-02") + "\r\n")
	}
	if len(groupNames) > 0 {
		parts := make([]string, 0, len(groupNames))
		for _, n := range groupNames {
			if n = strings.TrimSpace(n); n != "" {
				parts = append(parts, escape(n))
			}
		}
		if len(parts) > 0 {
			b.WriteString("CATEGORIES:" + strings.Join(parts, ",") + "\r\n")
		}
	}
	if c.Notes != "" {
		b.WriteString("NOTE:" + escape(c.Notes) + "\r\n")
	}
	b.WriteString("END:VCARD\r\n")
	_, err := io.WriteString(w, b.String())
	return err
}

// escape applies vCard backslash escaping to a single value.
func escape(s string) string {
	if !strings.ContainsAny(s, ",;\\\n\r") {
		return s
	}
	var b strings.Builder
	for _, r := range s {
		switch r {
		case '\\':
			b.WriteString(`\\`)
		case ',':
			b.WriteString(`\,`)
		case ';':
			b.WriteString(`\;`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			// drop CR; folded lines use CRLF and we already trim them
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}
