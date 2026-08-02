// Package letter holds the MIME helpers shared across the mail module: address
// parsing, RFC822 message building, and best-effort parsing of received mail
// into text/html bodies. It is a leaf package so inbound, outbound, inbox and
// the HTTP handler can all depend on it without cycles.
package letter

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/mail"
	"net/textproto"
	"strings"
	"time"
)

// Address is a named email address. It mirrors net/mail.Address but with JSON
// tags so it can be stored and served directly.
type Address struct {
	Name    string `json:"name,omitempty"`
	Address string `json:"address"`
}

// String formats the address as a RFC5322 mailbox ("Name <addr>" or "addr").
func (a Address) String() string {
	if a.Name == "" {
		return a.Address
	}
	return fmt.Sprintf("%s <%s>", quoteName(a.Name), a.Address)
}

// ParseAddressList parses a comma-separated RFC5322 address list into Addresses.
func ParseAddressList(s string) ([]Address, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	addrs, err := mail.ParseAddress(s)
	if err != nil {
		// mail.ParseAddress only handles a single address; use the list parser.
		list, lerr := mail.ParseAddressList(s)
		if lerr != nil {
			return nil, lerr
		}
		return toAddresses(list), nil
	}
	return []Address{{Name: addrs.Name, Address: addrs.Address}}, nil
}

func toAddresses(in []*mail.Address) []Address {
	out := make([]Address, 0, len(in))
	for _, a := range in {
		out = append(out, Address{Name: a.Name, Address: a.Address})
	}
	return out
}

// MarshalAddresses encodes an address slice as JSON for DB storage.
func MarshalAddresses(in []Address) (string, error) {
	if in == nil {
		return "[]", nil
	}
	b, err := json.Marshal(in)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// UnmarshalAddresses decodes a JSON address slice from DB storage.
func UnmarshalAddresses(s string) []Address {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	var out []Address
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		return nil
	}
	return out
}

// Outgoing is a fully-formed outbound message. Drivers format it for their
// transport; SMTP/SES use Raw (built by BuildRFC822) while HTTP APIs map the
// fields onto their JSON payloads.
type Outgoing struct {
	From       Address
	To         []Address
	Cc         []Address
	Bcc        []Address
	ReplyTo    []Address
	Subject    string
	Text       string
	HTML       string
	MessageID  string // generated if empty
	InReplyTo  string
	References []string
	Headers    textproto.MIMEHeader // extra headers (attachments, custom)
}

// Recipients returns To + Cc + Bcc as a flat list (for SMTP envelope).
func (o Outgoing) Recipients() []string {
	out := make([]string, 0, len(o.To)+len(o.Cc)+len(o.Bcc))
	for _, a := range o.To {
		out = append(out, a.Address)
	}
	for _, a := range o.Cc {
		out = append(out, a.Address)
	}
	for _, a := range o.Bcc {
		out = append(out, a.Address)
	}
	return out
}

// BuildRFC822 renders the outgoing message as RFC822 bytes. It chooses a
// multipart/alternative body when both text and html are present, else a plain
// text/plain or text/html part.
func BuildRFC822(o Outgoing) ([]byte, error) {
	if o.MessageID == "" {
		o.MessageID = NewMessageID(o.From.Address)
	}
	var buf bytes.Buffer
	h := textproto.MIMEHeader{}
	if o.Headers != nil {
		for k, vs := range o.Headers {
			for _, v := range vs {
				h.Add(k, v)
			}
		}
	}
	h.Set("Date", time.Now().UTC().Format(time.RFC1123Z))
	h.Set("Message-ID", "<"+o.MessageID+">")
	h.Set("From", o.From.String())
	h.Set("To", joinAddresses(o.To))
	if len(o.Cc) > 0 {
		h.Set("Cc", joinAddresses(o.Cc))
	}
	if len(o.ReplyTo) > 0 {
		h.Set("Reply-To", joinAddresses(o.ReplyTo))
	}
	h.Set("Subject", mime.QEncoding.Encode("utf-8", o.Subject))
	if o.InReplyTo != "" {
		h.Set("In-Reply-To", "<"+trimAngled(o.InReplyTo)+">")
	}
	if len(o.References) > 0 {
		refs := make([]string, len(o.References))
		for i, r := range o.References {
			refs[i] = "<" + trimAngled(r) + ">"
		}
		h.Set("References", strings.Join(refs, " "))
	}
	h.Set("MIME-Version", "1.0")

	hasText := o.Text != ""
	hasHTML := o.HTML != ""
	switch {
	case hasText && hasHTML:
		boundary := "airbrew_" + randHex(16)
		h.Set("Content-Type", "multipart/alternative; boundary=\""+boundary+"\"")
		writeHeaders(&buf, h)
		buf.WriteString("\r\n")
		mp := multipart.NewWriter(&buf)
		_ = mp.SetBoundary(boundary)
		if part, err := mp.CreatePart(textproto.MIMEHeader{
			"Content-Type":              {"text/plain; charset=utf-8"},
			"Content-Transfer-Encoding": {"8bit"},
		}); err == nil {
			_, _ = io.WriteString(part, o.Text)
		}
		if part, err := mp.CreatePart(textproto.MIMEHeader{
			"Content-Type":              {"text/html; charset=utf-8"},
			"Content-Transfer-Encoding": {"8bit"},
		}); err == nil {
			_, _ = io.WriteString(part, o.HTML)
		}
		_ = mp.Close()
	case hasHTML:
		h.Set("Content-Type", "text/html; charset=utf-8")
		h.Set("Content-Transfer-Encoding", "8bit")
		writeHeaders(&buf, h)
		buf.WriteString("\r\n")
		buf.WriteString(o.HTML)
	default:
		h.Set("Content-Type", "text/plain; charset=utf-8")
		h.Set("Content-Transfer-Encoding", "8bit")
		writeHeaders(&buf, h)
		buf.WriteString("\r\n")
		buf.WriteString(o.Text)
	}
	return buf.Bytes(), nil
}

func writeHeaders(buf *bytes.Buffer, h textproto.MIMEHeader) {
	for k, vs := range h {
		for _, v := range vs {
			buf.WriteString(k)
			buf.WriteString(": ")
			buf.WriteString(v)
			buf.WriteString("\r\n")
		}
	}
}

func joinAddresses(in []Address) string {
	parts := make([]string, 0, len(in))
	for _, a := range in {
		parts = append(parts, a.String())
	}
	return strings.Join(parts, ", ")
}

func trimAngled(s string) string { return strings.Trim(s, "<> ") }

func quoteName(name string) string {
	if strings.ContainsAny(name, "()<>@,;:\\\".[]") {
		return `"` + strings.ReplaceAll(name, `"`, `\"`) + `"`
	}
	return name
}

// NewMessageID generates a Message-ID value (without angle brackets) using the
// provided domain.
func NewMessageID(domain string) string {
	if i := strings.LastIndex(domain, "@"); i >= 0 {
		domain = domain[i+1:]
	}
	return randHex(16) + "." + randHex(8) + "@" + domain
}

// randHex returns n random bytes hex-encoded (crypto/rand).
func randHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand failure is unrecoverable.
		panic("letter: rand failed: " + err.Error())
	}
	return hex.EncodeToString(b)
}

// ParsedMessage is the result of parsing a raw RFC822 message into the fields
// the inbox stores.
type ParsedMessage struct {
	MessageID string
	Subject   string
	From      []Address
	To        []Address
	Cc        []Address
	Bcc       []Address
	ReplyTo   []Address
	Date      time.Time
	References []string // normalized RFC5322 References list
	Text      string
	HTML      string
}

// Parse reads a raw RFC822 message and extracts the header fields and the
// text/plain + text/html bodies (best effort over multipart).
func Parse(raw []byte) (*ParsedMessage, error) {
	if len(raw) == 0 {
		return nil, errors.New("letter: empty message")
	}
	msg, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("letter: read message: %w", err)
	}
	h := msg.Header
	out := &ParsedMessage{
		MessageID:  strings.Trim(h.Get("Message-ID"), "<> "),
		Subject:    decodeHeader(h.Get("Subject")),
		From:       safeList(decodeHeader(h.Get("From"))),
		To:         safeList(decodeHeader(h.Get("To"))),
		Cc:         safeList(decodeHeader(h.Get("Cc"))),
		Bcc:        safeList(decodeHeader(h.Get("Bcc"))),
		ReplyTo:    safeList(decodeHeader(h.Get("Reply-To"))),
		References: parseMsgIDList(h.Get("References"), h.Get("In-Reply-To")),
	}
	if d := h.Get("Date"); d != "" {
		if t, err := mail.ParseDate(d); err == nil {
			out.Date = t.UTC()
		}
	}
	text, html, err := bodies(h, msg.Body)
	if err != nil {
		return nil, err
	}
	out.Text = text
	out.HTML = html
	return out, nil
}

func safeList(s string) []Address {
	list, err := ParseAddressList(s)
	if err != nil {
		return nil
	}
	return list
}

// bodies extracts the text/plain and text/html parts, descending one level of
// multipart (multipart/alternative, multipart/mixed). Good enough for typical
// inbound mail without a full MIME walker.
func bodies(h mail.Header, body io.Reader) (text, html string, err error) {
	ct := h.Get("Content-Type")
	mediatype, params, perr := mime.ParseMediaType(ct)
	if perr != nil {
		// No Content-Type or unparseable: treat body as plain text.
		b, rerr := io.ReadAll(body)
		if rerr != nil {
			return "", "", rerr
		}
		return string(b), "", nil
	}
	switch {
	case strings.HasPrefix(mediatype, "multipart/"):
		mr := multipart.NewReader(body, params["boundary"])
		for {
			part, perr := mr.NextPart()
			if perr == io.EOF {
				return text, html, nil
			}
			if perr != nil {
				return text, html, perr
			}
			pct := part.Header.Get("Content-Type")
			pmed, _, _ := mime.ParseMediaType(pct)
			data, derr := readPart(part, part.Header.Get("Content-Transfer-Encoding"))
			if derr != nil {
				continue
			}
			switch {
			case strings.HasPrefix(pmed, "text/plain") && text == "":
				text = data
			case strings.HasPrefix(pmed, "text/html") && html == "":
				html = data
			case strings.HasPrefix(pmed, "multipart/"):
				// Nested multipart: recurse via a synthetic header.
				t, he, _ := bodies(mail.Header{"Content-Type": []string{pct}}, bytes.NewReader([]byte(data)))
				if t != "" && text == "" {
					text = t
				}
				if he != "" && html == "" {
					html = he
				}
			}
		}
	case mediatype == "text/html":
		data, rerr := readDecoded(body, params)
		if rerr != nil {
			return "", "", rerr
		}
		return "", data, nil
	default:
		data, rerr := readDecoded(body, params)
		if rerr != nil {
			return "", "", rerr
		}
		return data, "", nil
	}
}

func readDecoded(body io.Reader, params map[string]string) (string, error) {
	b, err := io.ReadAll(body)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func readPart(part *multipart.Part, cte string) (string, error) {
	b, err := io.ReadAll(part)
	if err != nil {
		return "", err
	}
	return decodeCTE(b, cte), nil
}

// decodeCTE reverses common Content-Transfer-Encodings.
func decodeCTE(b []byte, cte string) string {
	switch strings.ToLower(strings.TrimSpace(cte)) {
	case "base64":
		out, err := base64.StdEncoding.DecodeString(string(bytes.TrimSpace(b)))
		if err != nil {
			return string(b)
		}
		return string(out)
	case "quoted-printable":
		return decodeQP(b)
	default:
		return string(b)
	}
}

func decodeQP(b []byte) string {
	var buf bytes.Buffer
	for i := 0; i < len(b); i++ {
		c := b[i]
		if c == '=' {
			if i+2 < len(b) {
				var v byte
				_, err := fmt.Sscanf(string(b[i+1:i+3]), "%02X", &v)
				if err == nil {
					buf.WriteByte(v)
					i += 2
					continue
				}
			}
		}
		buf.WriteByte(c)
	}
	return buf.String()
}

// decodeHeader decodes an RFC2047-encoded header value.
func decodeHeader(s string) string {
	dec := mime.WordDecoder{}
	out, err := dec.DecodeHeader(s)
	if err != nil {
		return s
	}
	return out
}

// parseMsgIDList splits one or more whitespace-separated "<id>" fields (the
// RFC5322 References header plus an optional In-Reply-To) into a de-duplicated,
// lowercased, angle-bracket-stripped list in the order they appear.
func parseMsgIDList(fields ...string) []string {
	seen := map[string]struct{}{}
	var out []string
	for _, f := range fields {
		for _, token := range strings.Fields(f) {
			id := strings.ToLower(strings.Trim(token, "<>,;"))
			if id == "" {
				continue
			}
			if _, ok := seen[id]; ok {
				continue
			}
			seen[id] = struct{}{}
			out = append(out, id)
		}
	}
	return out
}

// ThreadKey returns the stable conversation key for a message given its own
// Message-ID, In-Reply-To and References. The oldest ancestor id wins; a
// message with no references starts a new thread keyed by its own id.
func ThreadKey(messageID, inReplyTo string, references []string) string {
	chain := parseMsgIDList(strings.Join(references, " "), inReplyTo)
	if len(chain) > 0 {
		return chain[0]
	}
	if messageID == "" {
		return "no-id"
	}
	return strings.ToLower(strings.Trim(messageID, "<> "))
}

