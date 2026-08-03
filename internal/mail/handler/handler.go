// Package handler exposes the HTTP endpoints for the mail module: mailbox and
// message management, sending, the inbound webhook receivers, and the admin
// provider-config surface. User endpoints require a browser session; webhooks
// are public but authenticated by the provider's configured secret; admin
// endpoints require an admin session (enforced by the caller wrapping the mux).
package handler

import (
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/mail"
	"strconv"
	"strings"

	"github.com/headercat/airbrew/internal/blob"
	"github.com/headercat/airbrew/internal/mail/inbox"
	"github.com/headercat/airbrew/internal/mail/letter"
	"github.com/headercat/airbrew/internal/mail/outbound"
	"github.com/headercat/airbrew/internal/mail/provider"
)

// Handler exposes the mail JSON endpoints.
type Handler struct {
	inbox *inbox.Service
	repo  *provider.Repository
	blobs blob.Store
}

// New builds a Handler.
func New(in *inbox.Service, repo *provider.Repository, blobs blob.Store) *Handler {
	return &Handler{inbox: in, repo: repo, blobs: blobs}
}

// RegisterRoutes mounts the authenticated user endpoints on mux.
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/mail/mailboxes", h.listMailboxes)
	mux.HandleFunc("POST /api/mail/mailboxes", h.createMailbox)
	mux.HandleFunc("DELETE /api/mail/mailboxes/{id}", h.deleteMailbox)

	mux.HandleFunc("GET /api/mail/counts", h.counts)
	mux.HandleFunc("GET /api/mail/threads", h.listThreads)
	mux.HandleFunc("GET /api/mail/messages", h.listMessages)
	mux.HandleFunc("GET /api/mail/messages/{id}", h.getMessage)
	mux.HandleFunc("PATCH /api/mail/messages/{id}", h.patchMessage)
	mux.HandleFunc("DELETE /api/mail/messages/{id}", h.deleteMessage)
	mux.HandleFunc("POST /api/mail/messages/{id}/retry", h.retryMessage)
	mux.HandleFunc("GET /api/mail/messages/{id}/raw", h.getRaw)
	mux.HandleFunc("GET /api/mail/messages/{id}/attachments", h.listMessageAttachments)

	mux.HandleFunc("POST /api/mail/send", h.send)
	mux.HandleFunc("POST /api/mail/drafts", h.createDraft)
	mux.HandleFunc("PATCH /api/mail/drafts/{id}", h.updateDraft)
	mux.HandleFunc("DELETE /api/mail/drafts/{id}", h.deleteDraft)

	mux.HandleFunc("POST /api/mail/attachments", h.uploadAttachment)
	mux.HandleFunc("GET /api/mail/attachments/{id}", h.downloadAttachment)
	mux.HandleFunc("DELETE /api/mail/attachments/{id}", h.deleteAttachment)
}

// --- mailboxes -------------------------------------------------------------

type mailboxResp struct {
	ID          string `json:"id"`
	Address     string `json:"address"`
	LocalPart   string `json:"local_part"`
	Domain      string `json:"domain"`
	DisplayName string `json:"display_name"`
	IsPrimary   bool   `json:"is_primary"`
	CreatedAt   string `json:"created_at"`
}

func toMailboxResp(mb *inbox.Mailbox) mailboxResp {
	return mailboxResp{
		ID: mb.ID, Address: mb.Address, LocalPart: mb.LocalPart, Domain: mb.Domain,
		DisplayName: mb.DisplayName, IsPrimary: mb.IsPrimary,
		CreatedAt: mb.CreatedAt.UTC().Format(timeRFC3339),
	}
}

func (h *Handler) listMailboxes(w http.ResponseWriter, r *http.Request) {
	sess, ok := requireSession(w, r)
	if !ok {
		return
	}
	mbs, err := h.inbox.ListMailboxes(r.Context(), sess.UserID)
	if err != nil {
		writeErr(w, err)
		return
	}
	out := make([]mailboxResp, 0, len(mbs))
	for _, mb := range mbs {
		out = append(out, toMailboxResp(mb))
	}
	jsonResp(w, http.StatusOK, map[string]any{"mailboxes": out})
}

type createMailboxReq struct {
	Address     string `json:"address"`
	DisplayName string `json:"display_name"`
	IsPrimary   bool   `json:"is_primary"`
}

func (h *Handler) createMailbox(w http.ResponseWriter, r *http.Request) {
	sess, ok := requireSession(w, r)
	if !ok {
		return
	}
	var req createMailboxReq
	if err := decodeJSON(r, &req); err != nil {
		respondErr(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	mb, err := h.inbox.CreateMailbox(r.Context(), inbox.NewMailboxInput{
		UserID: sess.UserID, Address: req.Address, DisplayName: req.DisplayName, IsPrimary: req.IsPrimary,
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	jsonResp(w, http.StatusCreated, toMailboxResp(mb))
}

func (h *Handler) deleteMailbox(w http.ResponseWriter, r *http.Request) {
	sess, ok := requireSession(w, r)
	if !ok {
		return
	}
	if err := h.inbox.DeleteMailbox(r.Context(), sess.UserID, r.PathValue("id")); err != nil {
		writeErr(w, err)
		return
	}
	jsonResp(w, http.StatusOK, map[string]bool{"ok": true})
}

// --- messages --------------------------------------------------------------

type messageResp struct {
	ID          string           `json:"id"`
	MailboxID   string           `json:"mailbox_id"`
	MessageID   string           `json:"message_id"`
	ThreadID    string           `json:"thread_id"`
	InReplyTo   string           `json:"in_reply_to"`
	References  []string         `json:"references"`
	Subject     string           `json:"subject"`
	From        letter.Address   `json:"from"`
	To          []letter.Address `json:"to"`
	Cc          []letter.Address `json:"cc"`
	Bcc         []letter.Address `json:"bcc"`
	ReplyTo     []letter.Address `json:"reply_to"`
	Direction   string           `json:"direction"`
	BodyText    string           `json:"body_text"`
	BodyHTML    string           `json:"body_html"`
	IsRead      bool             `json:"is_read"`
	IsStarred   bool             `json:"is_starred"`
	IsDraft     bool             `json:"is_draft"`
	IsOutbox    bool             `json:"is_outbox"`
	SizeBytes   int64            `json:"size_bytes"`
	ReceivedAt  string           `json:"received_at,omitempty"`
	SentAt      string           `json:"sent_at,omitempty"`
	CreatedAt   string           `json:"created_at"`
	Attachments []attachmentResp `json:"attachments,omitempty"`
}

func toMessageResp(m *inbox.Message) messageResp {
	out := messageResp{
		ID: m.ID, MailboxID: m.MailboxID, MessageID: m.MessageID,
		ThreadID: m.ThreadID, InReplyTo: m.InReplyTo, References: m.References,
		Subject: m.Subject,
		From:    m.From, To: m.To, Cc: m.Cc, Bcc: m.Bcc, ReplyTo: m.ReplyTo,
		Direction: string(m.Direction), BodyText: m.BodyText, BodyHTML: m.BodyHTML,
		IsRead: m.IsRead, IsStarred: m.IsStarred, IsDraft: m.IsDraft,
		IsOutbox:  m.IsOutbox,
		SizeBytes: m.SizeBytes,
		CreatedAt: m.CreatedAt.UTC().Format(timeRFC3339),
	}
	if m.ReceivedAt != nil {
		out.ReceivedAt = m.ReceivedAt.UTC().Format(timeRFC3339)
	}
	if m.SentAt != nil {
		out.SentAt = m.SentAt.UTC().Format(timeRFC3339)
	}
	if out.To == nil {
		out.To = []letter.Address{}
	}
	if out.References == nil {
		out.References = []string{}
	}
	return out
}

type attachmentResp struct {
	ID          string `json:"id"`
	MessageID   string `json:"message_id,omitempty"`
	Filename    string `json:"filename"`
	ContentType string `json:"content_type"`
	ContentID   string `json:"content_id,omitempty"`
	Inline      bool   `json:"inline"`
	SizeBytes   int64  `json:"size_bytes"`
	CreatedAt   string `json:"created_at"`
	DownloadURL string `json:"download_url"`
}

func toAttachmentResp(a inbox.Attachment) attachmentResp {
	return attachmentResp{
		ID: a.ID, MessageID: a.MessageID, Filename: a.Filename,
		ContentType: a.ContentType, ContentID: a.ContentID, Inline: a.Inline,
		SizeBytes: a.SizeBytes, CreatedAt: a.CreatedAt.UTC().Format(timeRFC3339),
		DownloadURL: "/api/mail/attachments/" + a.ID,
	}
}

func (h *Handler) listMessages(w http.ResponseWriter, r *http.Request) {
	sess, ok := requireSession(w, r)
	if !ok {
		return
	}
	f := inbox.ListFilter{
		UserID:    sess.UserID,
		MailboxID: r.URL.Query().Get("mailbox"),
		Folder:    r.URL.Query().Get("folder"),
		ThreadID:  r.URL.Query().Get("thread"),
		Query:     r.URL.Query().Get("q"),
		Limit:     parseInt(r.URL.Query().Get("limit")),
		Offset:    parseInt(r.URL.Query().Get("offset")),
	}
	msgs, total, err := h.inbox.ListMessagesWithTotal(r.Context(), f)
	if err != nil {
		writeErr(w, err)
		return
	}
	out := make([]messageResp, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, toMessageResp(m))
	}
	jsonResp(w, http.StatusOK, map[string]any{"messages": out, "total": total})
}

func (h *Handler) counts(w http.ResponseWriter, r *http.Request) {
	sess, ok := requireSession(w, r)
	if !ok {
		return
	}
	c, err := h.inbox.Counts(r.Context(), sess.UserID, r.URL.Query().Get("mailbox"))
	if err != nil {
		writeErr(w, err)
		return
	}
	jsonResp(w, http.StatusOK, c)
}

type threadResp struct {
	ThreadID    string         `json:"thread_id"`
	Subject     string         `json:"subject"`
	From        letter.Address `json:"from"`
	LastAt      string         `json:"last_at"`
	Count       int            `json:"count"`
	UnreadCount int            `json:"unread_count"`
}

func (h *Handler) listThreads(w http.ResponseWriter, r *http.Request) {
	sess, ok := requireSession(w, r)
	if !ok {
		return
	}
	threads, err := h.inbox.ListThreads(r.Context(), sess.UserID,
		r.URL.Query().Get("mailbox"),
		parseInt(r.URL.Query().Get("limit")),
		parseInt(r.URL.Query().Get("offset")))
	if err != nil {
		writeErr(w, err)
		return
	}
	out := make([]threadResp, 0, len(threads))
	for _, t := range threads {
		out = append(out, threadResp{
			ThreadID: t.ThreadID, Subject: t.Subject, From: t.From,
			LastAt: t.LastAt.UTC().Format(timeRFC3339),
			Count:  t.Count, UnreadCount: t.UnreadCount,
		})
	}
	jsonResp(w, http.StatusOK, map[string]any{"threads": out})
}

func (h *Handler) getMessage(w http.ResponseWriter, r *http.Request) {
	sess, ok := requireSession(w, r)
	if !ok {
		return
	}
	m, err := h.inbox.GetMessage(r.Context(), sess.UserID, r.PathValue("id"))
	if err != nil {
		writeErr(w, err)
		return
	}
	resp := toMessageResp(m)
	atts, err := h.inbox.ListAttachments(r.Context(), sess.UserID, m.ID)
	if err != nil {
		writeErr(w, err)
		return
	}
	for _, a := range atts {
		resp.Attachments = append(resp.Attachments, toAttachmentResp(a))
	}
	jsonResp(w, http.StatusOK, resp)
}

type patchMessageReq struct {
	IsRead    *bool `json:"is_read"`
	IsStarred *bool `json:"is_starred"`
}

func (h *Handler) patchMessage(w http.ResponseWriter, r *http.Request) {
	sess, ok := requireSession(w, r)
	if !ok {
		return
	}
	var req patchMessageReq
	if err := decodeJSON(r, &req); err != nil {
		respondErr(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if err := h.inbox.PatchFlags(r.Context(), sess.UserID, r.PathValue("id"), inbox.FlagPatch{
		IsRead: req.IsRead, IsStarred: req.IsStarred,
	}); err != nil {
		writeErr(w, err)
		return
	}
	jsonResp(w, http.StatusOK, map[string]bool{"ok": true})
}

func (h *Handler) deleteMessage(w http.ResponseWriter, r *http.Request) {
	sess, ok := requireSession(w, r)
	if !ok {
		return
	}
	if err := h.inbox.DeleteMessage(r.Context(), sess.UserID, r.PathValue("id")); err != nil {
		writeErr(w, err)
		return
	}
	jsonResp(w, http.StatusOK, map[string]bool{"ok": true})
}

func (h *Handler) retryMessage(w http.ResponseWriter, r *http.Request) {
	sess, ok := requireSession(w, r)
	if !ok {
		return
	}
	sender, err := outbound.Resolve(r.Context(), h.repo)
	if err != nil {
		writeErr(w, err)
		return
	}
	msg, err := h.inbox.RetrySend(r.Context(), sess.UserID, r.PathValue("id"), sender)
	if err != nil {
		if msg != nil && msg.IsOutbox {
			jsonResp(w, http.StatusAccepted, toMessageResp(msg))
			return
		}
		writeErr(w, err)
		return
	}
	jsonResp(w, http.StatusOK, toMessageResp(msg))
}

func (h *Handler) getRaw(w http.ResponseWriter, r *http.Request) {
	sess, ok := requireSession(w, r)
	if !ok {
		return
	}
	m, err := h.inbox.GetMessage(r.Context(), sess.UserID, r.PathValue("id"))
	if err != nil {
		writeErr(w, err)
		return
	}
	if m.RawPath == "" || h.blobs == nil {
		respondErr(w, http.StatusNotFound, "not_found", "raw message not stored")
		return
	}
	body, _, err := h.blobs.Open(r.Context(), m.RawPath)
	if err != nil {
		respondErr(w, http.StatusNotFound, "not_found", "raw message missing")
		return
	}
	defer body.Close()
	w.Header().Set("Content-Type", "message/rfc822")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Disposition",
		mime.FormatMediaType("attachment", map[string]string{"filename": m.ID + ".eml"}))
	if _, err := io.Copy(w, body); err != nil {
		return
	}
}

func (h *Handler) listMessageAttachments(w http.ResponseWriter, r *http.Request) {
	sess, ok := requireSession(w, r)
	if !ok {
		return
	}
	atts, err := h.inbox.ListAttachments(r.Context(), sess.UserID, r.PathValue("id"))
	if err != nil {
		writeErr(w, err)
		return
	}
	out := make([]attachmentResp, 0, len(atts))
	for _, a := range atts {
		out = append(out, toAttachmentResp(a))
	}
	jsonResp(w, http.StatusOK, map[string]any{"attachments": out})
}

func (h *Handler) uploadAttachment(w http.ResponseWriter, r *http.Request) {
	sess, ok := requireSession(w, r)
	if !ok {
		return
	}
	// Hard ceiling on the whole request body so a huge upload is rejected
	// before ParseMultipartForm can spill it to temp disk. Matches the
	// service-level attachment size cap (25 MiB) plus a small envelope.
	r.Body = http.MaxBytesReader(w, r.Body, 25<<20+512)
	if err := r.ParseMultipartForm(25 << 20); err != nil {
		respondErr(w, http.StatusBadRequest, "invalid_request", "expected multipart form with file under 25 MiB")
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		respondErr(w, http.StatusBadRequest, "invalid_request", "file is required")
		return
	}
	defer file.Close()
	contentType := header.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	a, err := h.inbox.CreateAttachment(r.Context(), sess.UserID, header.Filename, contentType, file)
	if err != nil {
		writeErr(w, err)
		return
	}
	jsonResp(w, http.StatusCreated, toAttachmentResp(a))
}

func (h *Handler) downloadAttachment(w http.ResponseWriter, r *http.Request) {
	sess, ok := requireSession(w, r)
	if !ok {
		return
	}
	a, body, err := h.inbox.GetAttachment(r.Context(), sess.UserID, r.PathValue("id"))
	if err != nil {
		writeErr(w, err)
		return
	}
	defer body.Close()
	ct := strings.TrimSpace(a.ContentType)
	if ct == "" {
		ct = "application/octet-stream"
	}
	w.Header().Set("Content-Type", ct)
	dispType := "attachment"
	if a.Inline {
		// Inline parts (cid: images, embedded logos) must be served with
		// Content-Disposition: inline or the browser refuses to render
		// them inside the message body's <img> tags.
		dispType = "inline"
	}
	w.Header().Set("Content-Disposition",
		mime.FormatMediaType(dispType, map[string]string{"filename": a.Filename}))
	if a.SizeBytes > 0 {
		w.Header().Set("Content-Length", parseContentLength(a.SizeBytes))
	}
	_, _ = io.Copy(w, body)
}

func (h *Handler) deleteAttachment(w http.ResponseWriter, r *http.Request) {
	sess, ok := requireSession(w, r)
	if !ok {
		return
	}
	if err := h.inbox.DeleteAttachment(r.Context(), sess.UserID, r.PathValue("id")); err != nil {
		writeErr(w, err)
		return
	}
	jsonResp(w, http.StatusOK, map[string]bool{"ok": true})
}

// --- compose (send + drafts) ----------------------------------------------

type addressInput struct {
	Name    string `json:"name"`
	Address string `json:"address"`
}

// composeReq is the shared body shape for /api/mail/send and /api/mail/drafts.
// The "send" field is only consulted by the drafts endpoints, where true
// means "save then immediately hand the draft to the active outbound driver".
type composeReq struct {
	MailboxID     string         `json:"mailbox_id"`
	To            []addressInput `json:"to"`
	Cc            []addressInput `json:"cc"`
	Bcc           []addressInput `json:"bcc"`
	ReplyTo       []addressInput `json:"reply_to"`
	Subject       string         `json:"subject"`
	Text          string         `json:"text"`
	HTML          string         `json:"html"`
	InReplyTo     string         `json:"in_reply_to"`
	References    []string       `json:"references"`
	AttachmentIDs []string       `json:"attachment_ids"`
	Send          bool           `json:"send"`
}

func (q composeReq) toInput() (inbox.SendInput, error) {
	to, err := toAddresses(q.To)
	if err != nil {
		return inbox.SendInput{}, err
	}
	cc, err := toAddresses(q.Cc)
	if err != nil {
		return inbox.SendInput{}, err
	}
	bcc, err := toAddresses(q.Bcc)
	if err != nil {
		return inbox.SendInput{}, err
	}
	rt, err := toAddresses(q.ReplyTo)
	if err != nil {
		return inbox.SendInput{}, err
	}
	return inbox.SendInput{
		MailboxID:     q.MailboxID,
		To:            to,
		Cc:            cc,
		Bcc:           bcc,
		ReplyTo:       rt,
		Subject:       q.Subject,
		Text:          q.Text,
		HTML:          q.HTML,
		InReplyTo:     q.InReplyTo,
		References:    q.References,
		AttachmentIDs: q.AttachmentIDs,
	}, nil
}

func (h *Handler) send(w http.ResponseWriter, r *http.Request) {
	sess, ok := requireSession(w, r)
	if !ok {
		return
	}
	var req composeReq
	if err := decodeJSON(r, &req); err != nil {
		respondErr(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	in, err := req.toInput()
	if err != nil {
		respondErr(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	sender, err := outbound.Resolve(r.Context(), h.repo)
	if err != nil {
		writeErr(w, err)
		return
	}
	msg, err := h.inbox.Send(r.Context(), sess.UserID, in, sender)
	if err != nil {
		// A failed send still leaves a persisted outbox row; return it so
		// the SPA can show which message failed and offer retry instead of
		// surfacing a bare error and forcing the user to dig.
		if msg != nil && msg.IsOutbox {
			jsonResp(w, http.StatusAccepted, toMessageResp(msg))
			return
		}
		writeErr(w, err)
		return
	}
	jsonResp(w, http.StatusCreated, toMessageResp(msg))
}

func toAddresses(in []addressInput) ([]letter.Address, error) {
	out := make([]letter.Address, 0, len(in))
	for _, a := range in {
		addr := strings.TrimSpace(a.Address)
		if addr == "" {
			return nil, fmt.Errorf("%w: empty address", inbox.ErrInvalidInput)
		}
		// mail.ParseAddress accepts "Name <addr>" and bare "addr"; reject
		// anything that is not a syntactically valid RFC5322 mailbox so a
		// typo (alice@exampl) is caught here with a clear 400 instead of a
		// generic 5xx from the upstream driver.
		parsed, err := mail.ParseAddress(addr)
		if err != nil {
			return nil, fmt.Errorf("%w: %v", inbox.ErrInvalidInput, err)
		}
		name := strings.TrimSpace(a.Name)
		if name == "" {
			name = parsed.Name
		}
		out = append(out, letter.Address{Name: name, Address: parsed.Address})
	}
	return out, nil
}

func parseContentLength(n int64) string {
	return strconv.FormatInt(n, 10)
}

// --- drafts ---------------------------------------------------------------

func (h *Handler) createDraft(w http.ResponseWriter, r *http.Request) {
	sess, ok := requireSession(w, r)
	if !ok {
		return
	}
	var req composeReq
	if err := decodeJSON(r, &req); err != nil {
		respondErr(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	in, err := req.toInput()
	if err != nil {
		respondErr(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if req.Send {
		sender, err := outbound.Resolve(r.Context(), h.repo)
		if err != nil {
			writeErr(w, err)
			return
		}
		draft, err := h.inbox.SaveDraft(r.Context(), sess.UserID, "", in)
		if err != nil {
			writeErr(w, err)
			return
		}
		sent, err := h.inbox.SendDraft(r.Context(), sess.UserID, draft.ID, sender)
		if err != nil {
			if sent != nil && sent.IsOutbox {
				jsonResp(w, http.StatusAccepted, toMessageResp(sent))
				return
			}
			writeErr(w, err)
			return
		}
		jsonResp(w, http.StatusCreated, toMessageResp(sent))
		return
	}
	draft, err := h.inbox.SaveDraft(r.Context(), sess.UserID, "", in)
	if err != nil {
		writeErr(w, err)
		return
	}
	jsonResp(w, http.StatusCreated, toMessageResp(draft))
}

func (h *Handler) updateDraft(w http.ResponseWriter, r *http.Request) {
	sess, ok := requireSession(w, r)
	if !ok {
		return
	}
	var req composeReq
	if err := decodeJSON(r, &req); err != nil {
		respondErr(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	in, err := req.toInput()
	if err != nil {
		respondErr(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if req.Send {
		sender, err := outbound.Resolve(r.Context(), h.repo)
		if err != nil {
			writeErr(w, err)
			return
		}
		if _, err := h.inbox.SaveDraft(r.Context(), sess.UserID, r.PathValue("id"), in); err != nil {
			writeErr(w, err)
			return
		}
		sent, err := h.inbox.SendDraft(r.Context(), sess.UserID, r.PathValue("id"), sender)
		if err != nil {
			if sent != nil && sent.IsOutbox {
				jsonResp(w, http.StatusAccepted, toMessageResp(sent))
				return
			}
			writeErr(w, err)
			return
		}
		jsonResp(w, http.StatusOK, toMessageResp(sent))
		return
	}
	draft, err := h.inbox.SaveDraft(r.Context(), sess.UserID, r.PathValue("id"), in)
	if err != nil {
		writeErr(w, err)
		return
	}
	jsonResp(w, http.StatusOK, toMessageResp(draft))
}

func (h *Handler) deleteDraft(w http.ResponseWriter, r *http.Request) {
	sess, ok := requireSession(w, r)
	if !ok {
		return
	}
	if err := h.inbox.DeleteMessage(r.Context(), sess.UserID, r.PathValue("id")); err != nil {
		writeErr(w, err)
		return
	}
	jsonResp(w, http.StatusOK, map[string]bool{"ok": true})
}
