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
	Attachments []Attachment
	Headers    textproto.MIMEHeader // extra headers (attachments, custom)
}

// Attachment is one file to attach to an outbound message. Data is the raw
// bytes of the file; BuildRFC822 base64-encodes it into a MIME part.
type Attachment struct {
	Filename    string
	ContentType string
	ContentID   string // for inline (embedded) images
	Inline      bool
	Data        []byte
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

	// Build the message body entity (its Content-Type + raw bytes), independent
	// of whether attachments will wrap it in multipart/mixed.
	bodyCT, bodyBytes := buildBody(o.Text, o.HTML, hasText, hasHTML)

	if len(o.Attachments) == 0 {
		h.Set("Content-Type", bodyCT)
		writeHeaders(&buf, h)
		buf.WriteString("\r\n")
		buf.Write(bodyBytes)
		return buf.Bytes(), nil
	}

	// multipart/mixed: first part is the body, the rest are attachments.
	mixedBoundary := "airbrew_" + randHex(16)
	h.Set("Content-Type", "multipart/mixed; boundary=\""+mixedBoundary+"\"")
	writeHeaders(&buf, h)
	buf.WriteString("\r\n")
	mp := multipart.NewWriter(&buf)
	_ = mp.SetBoundary(mixedBoundary)
	if part, err := mp.CreatePart(textproto.MIMEHeader{"Content-Type": {bodyCT}}); err == nil {
		_, _ = part.Write(bodyBytes)
	}
	for _, a := range o.Attachments {
		ct := a.ContentType
		if ct == "" {
			ct = "application/octet-stream"
		}
		hdr := textproto.MIMEHeader{
			"Content-Type":              {fmt.Sprintf("%s; name=%q", ct, a.Filename)},
			"Content-Transfer-Encoding": {"base64"},
			"Content-Disposition":       {fmt.Sprintf("attachment; filename=%q", a.Filename)},
		}
		if a.Inline || a.ContentID != "" {
			hdr.Set("Content-Disposition", fmt.Sprintf("inline; filename=%q", a.Filename))
			if a.ContentID != "" {
				hdr.Set("Content-ID", "<"+a.ContentID+">")
			}
		}
		part, err := mp.CreatePart(hdr)
		if err != nil {
			continue
		}
		b64 := base64.StdEncoding.EncodeToString(a.Data)
		// Wrap at 76 chars per RFC2045.
		for i := 0; i < len(b64); i += 76 {
			end := i + 76
			if end > len(b64) {
				end = len(b64)
			}
			_, _ = part.Write([]byte(b64[i:end] + "\r\n"))
		}
	}
	_ = mp.Close()
	return buf.Bytes(), nil
}

// buildBody renders the text/html body entity and returns its Content-Type
// header value plus raw bytes (no leading headers, just the entity body).
func buildBody(text, html string, hasText, hasHTML bool) (string, []byte) {
	switch {
	case hasText && hasHTML:
		boundary := "airbrew_" + randHex(16)
		var buf bytes.Buffer
		mp := multipart.NewWriter(&buf)
		_ = mp.SetBoundary(boundary)
		if part, err := mp.CreatePart(textproto.MIMEHeader{
			"Content-Type":              {"text/plain; charset=utf-8"},
			"Content-Transfer-Encoding": {"8bit"},
		}); err == nil {
			_, _ = io.WriteString(part, text)
		}
		if part, err := mp.CreatePart(textproto.MIMEHeader{
			"Content-Type":              {"text/html; charset=utf-8"},
			"Content-Transfer-Encoding": {"8bit"},
		}); err == nil {
			_, _ = io.WriteString(part, html)
		}
		_ = mp.Close()
		return "multipart/alternative; boundary=\"" + boundary + "\"", buf.Bytes()
	case hasHTML:
		return "text/html; charset=utf-8", []byte(html)
	default:
		return "text/plain; charset=utf-8", []byte(text)
	}
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
	Attachments []ParsedAttachment
}

// ParsedAttachment is one decoded MIME part treated as an attachment or inline
// image, surfaced by Parse.
type ParsedAttachment struct {
	Filename    string
	ContentType string
	ContentID   string
	Disposition string // "attachment" (default) or "inline"
	Data        []byte
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
	text, html, atts, err := extract(h, msg.Body)
	if err != nil {
		return nil, err
	}
	out.Text = text
	out.HTML = html
	out.Attachments = atts
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
// extract walks a message body, returning the first text/plain and text/html
// bodies plus every part that looks like an attachment (Content-Disposition
// attachment/inline, or a non-text part with a filename). It recurses into
// nested multipart containers.
func extract(h mail.Header, body io.Reader) (text, html string, atts []ParsedAttachment, err error) {
	ct := h.Get("Content-Type")
	mediatype, params, perr := mime.ParseMediaType(ct)
	if perr != nil {
		// No Content-Type or unparseable: treat body as plain text.
		b, rerr := io.ReadAll(body)
		if rerr != nil {
			return "", "", nil, rerr
		}
		return string(b), "", nil, nil
	}
	if !strings.HasPrefix(mediatype, "multipart/") {
		// Leaf at top level: text/plain, text/html, or a single attachment.
		data, rerr := readDecoded(body)
		if rerr != nil {
			return "", "", nil, rerr
		}
		switch {
		case strings.HasPrefix(mediatype, "text/plain"):
			return data, "", nil, nil
		case strings.HasPrefix(mediatype, "text/html"):
			return "", data, nil, nil
		default:
			return "", "", []ParsedAttachment{{
				Filename:    params["name"],
				ContentType: mediatype,
				Data:        []byte(data),
			}}, nil
		}
	}
	mr := multipart.NewReader(body, params["boundary"])
	for {
		part, perr := mr.NextPart()
		if perr == io.EOF {
			return text, html, atts, nil
		}
		if perr != nil {
			return text, html, atts, perr
		}
		pct := part.Header.Get("Content-Type")
		pmed, pparams, _ := mime.ParseMediaType(pct)
		raw, derr := io.ReadAll(part)
		if derr != nil {
			continue
		}
		data := decodeCTE(raw, part.Header.Get("Content-Transfer-Encoding"))
		switch {
		case strings.HasPrefix(pmed, "multipart/"):
			// Nested container: recurse over the already-decoded bytes.
			t, he, sub, _ := extract(
				mail.Header{"Content-Type": []string{pct}},
				bytes.NewReader([]byte(data)),
			)
			if t != "" && text == "" {
				text = t
			}
			if he != "" && html == "" {
				html = he
			}
			atts = append(atts, sub...)
		case isAttachment(part.Header, pmed):
			atts = append(atts, ParsedAttachment{
				Filename:    filenameOf(part.Header, pparams),
				ContentType: pmed,
				ContentID:   strings.Trim(part.Header.Get("Content-ID"), "<> "),
				Disposition: dispositionOf(part.Header),
				Data:        []byte(data),
			})
		case strings.HasPrefix(pmed, "text/plain") && text == "":
			text = data
		case strings.HasPrefix(pmed, "text/html") && html == "":
			html = data
		}
	}
}

// isAttachment reports whether a MIME part should be treated as an attachment:
// it has Content-Disposition attachment/inline, or a filename parameter, or a
// Content-ID (inline image), and is not one of the body text types.
func isAttachment(hdr textproto.MIMEHeader, mediatype string) bool {
	if strings.HasPrefix(mediatype, "text/plain") || strings.HasPrefix(mediatype, "text/html") {
		// A text part with a filename is still an attachment (e.g. an attached
		// .txt) unless it is the body.
		disp, params, _ := mime.ParseMediaType(hdr.Get("Content-Disposition"))
		if disp == "attachment" {
			return true
		}
		_, ctParams, _ := mime.ParseMediaType(hdr.Get("Content-Type"))
		return params["filename"] != "" || ctParams["name"] != ""
	}
	return true // any non-text part is an attachment
}

func filenameOf(hdr textproto.MIMEHeader, ctParams map[string]string) string {
	_, dparams, _ := mime.ParseMediaType(hdr.Get("Content-Disposition"))
	if n := decodeHeader(dparams["filename"]); n != "" {
		return n
	}
	if n := decodeHeader(ctParams["name"]); n != "" {
		return n
	}
	return ""
}

func dispositionOf(hdr textproto.MIMEHeader) string {
	disp, _, _ := mime.ParseMediaType(hdr.Get("Content-Disposition"))
	if disp == "inline" {
		return "inline"
	}
	return "attachment"
}

func readDecoded(body io.Reader) (string, error) {
	b, err := io.ReadAll(body)
	if err != nil {
		return "", err
	}
	return string(b), nil
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

