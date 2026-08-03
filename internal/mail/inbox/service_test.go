package inbox_test

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/headercat/airbrew/internal/blob"
	"github.com/headercat/airbrew/internal/db"
	"github.com/headercat/airbrew/internal/mail/inbox"
	"github.com/headercat/airbrew/internal/mail/letter"
)

func newService(t *testing.T) (*inbox.Service, context.Context) {
	t.Helper()
	return newServiceWithBlob(t, nil)
}

func newServiceWithBlob(t *testing.T, blobs blob.Store) (*inbox.Service, context.Context) {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "mail.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	if err := d.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	for _, uid := range []string{"u1", "u2"} {
		if _, err := d.DB.Exec(
			`INSERT INTO users (id, public_subject, email) VALUES (?, ?, ?)`,
			uid, uid, uid+"@airbrew.local"); err != nil {
			t.Fatalf("seed user %s: %v", uid, err)
		}
	}
	repo := inbox.NewRepository(d.DB)
	return inbox.NewService(repo, blobs), context.Background()
}

func mustCreateMailbox(t *testing.T, s *inbox.Service, ctx context.Context, userID, addr string) *inbox.Mailbox {
	t.Helper()
	mb, err := s.CreateMailbox(ctx, inbox.NewMailboxInput{UserID: userID, Address: addr})
	if err != nil {
		t.Fatalf("create mailbox: %v", err)
	}
	return mb
}

func rawMsg(messageID, refs, to string) []byte {
	headers := "From: bob@ext.com\r\n"
	if to != "" {
		headers += "To: " + to + "\r\n"
	}
	headers += "Subject: test\r\n"
	if messageID != "" {
		headers += "Message-ID: <" + messageID + ">\r\n"
	}
	if refs != "" {
		headers += "References: <" + refs + ">\r\n"
	}
	headers += "Content-Type: text/plain; charset=utf-8\r\n\r\nbody"
	return []byte(headers)
}

// TestIngestDedup ensures a repeated Message-ID within a mailbox is rejected
// with ErrDuplicate so webhook retries / IMAP re-fetches cannot double-store.
func TestIngestDedup(t *testing.T) {
	s, ctx := newService(t)
	const uid = "u1"
	mustCreateMailbox(t, s, ctx, uid, "alice@airbrew.local")

	if _, err := s.Ingest(ctx, "alice@airbrew.local", rawMsg("m1", "", "alice@airbrew.local"), time.Now()); err != nil {
		t.Fatalf("first ingest: %v", err)
	}
	_, err := s.Ingest(ctx, "alice@airbrew.local", rawMsg("m1", "", "alice@airbrew.local"), time.Now())
	if err != inbox.ErrDuplicate {
		t.Fatalf("second ingest err = %v, want ErrDuplicate", err)
	}
}

// TestIngestThreadGrouping verifies a reply chains onto its parent thread.
func TestIngestThreadGrouping(t *testing.T) {
	s, ctx := newService(t)
	const uid = "u1"
	mustCreateMailbox(t, s, ctx, uid, "alice@airbrew.local")

	if _, err := s.Ingest(ctx, "alice@airbrew.local", rawMsg("orig1", "", "alice@airbrew.local"), time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Ingest(ctx, "alice@airbrew.local", rawMsg("rep1", "orig1", "alice@airbrew.local"), time.Now()); err != nil {
		t.Fatal(err)
	}
	threads, err := s.ListThreads(ctx, uid, "", 10, 0)
	if err != nil {
		t.Fatalf("list threads: %v", err)
	}
	if len(threads) != 1 {
		t.Fatalf("got %d threads, want 1", len(threads))
	}
	if threads[0].ThreadID != "orig1" || threads[0].Count != 2 {
		t.Fatalf("thread = %+v, want thread_id=orig1 count=2", threads[0])
	}

	// The thread filter must return both messages.
	msgs, err := s.ListMessages(ctx, inbox.ListFilter{UserID: uid, ThreadID: "orig1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 2 {
		t.Fatalf("thread messages = %d, want 2", len(msgs))
	}
}

// TestIngestRoutingByToCc checks fallback routing when the envelope recipient
// is empty — the To header should still resolve a known mailbox.
func TestIngestRoutingByToCc(t *testing.T) {
	s, ctx := newService(t)
	const uid = "u1"
	mustCreateMailbox(t, s, ctx, uid, "alice@airbrew.local")

	// Empty envelope recipient; rely on To header routing.
	if _, err := s.Ingest(ctx, "", rawMsg("m2", "", "alice@airbrew.local"), time.Now()); err != nil {
		t.Fatalf("ingest without envelope: %v", err)
	}
	// Unknown recipient and unknown To header -> ErrMailboxNotFound.
	if _, err := s.Ingest(ctx, "nobody@nowhere.test", rawMsg("m3", "", "stranger@nowhere.test"), time.Now()); err != inbox.ErrMailboxNotFound {
		t.Fatalf("err = %v, want ErrMailboxNotFound", err)
	}
}

// TestSendThreading ensures an outbound reply shares the inbound thread.
func TestSendThreading(t *testing.T) {
	s, ctx := newService(t)
	const uid = "u1"
	mb := mustCreateMailbox(t, s, ctx, uid, "alice@airbrew.local")

	// Receive then reply.
	if _, err := s.Ingest(ctx, "alice@airbrew.local", rawMsg("orig2", "", "alice@airbrew.local"), time.Now()); err != nil {
		t.Fatal(err)
	}
	sent, err := s.Send(ctx, uid, inbox.SendInput{
		MailboxID: mb.ID,
		To:        []letter.Address{{Address: "bob@ext.com"}},
		Subject:   "Re: test", Text: "reply body",
		InReplyTo: "orig2", References: []string{"orig2"},
	}, stubSender{})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if sent.ThreadID != "orig2" {
		t.Fatalf("sent thread_id = %q, want orig2", sent.ThreadID)
	}
	threads, _ := s.ListThreads(ctx, uid, "", 10, 0)
	if len(threads) != 1 {
		t.Fatalf("expected single thread, got %d", len(threads))
	}
}

// TestSendLinksPendingAttachments verifies composer uploads become MIME parts
// and are linked to the sent message record.
func TestSendLinksPendingAttachments(t *testing.T) {
	blobs, err := blob.NewLocal(t.TempDir())
	if err != nil {
		t.Fatalf("blob store: %v", err)
	}
	s, ctx := newServiceWithBlob(t, blobs)
	const uid = "u1"
	mb := mustCreateMailbox(t, s, ctx, uid, "alice@airbrew.local")

	att, err := s.CreateAttachment(ctx, uid, "notes.txt", "text/plain", strings.NewReader("ship it"))
	if err != nil {
		t.Fatalf("create attachment: %v", err)
	}
	sender := &captureSender{}
	msg, err := s.Send(ctx, uid, inbox.SendInput{
		MailboxID:     mb.ID,
		To:            []letter.Address{{Address: "bob@ext.com"}},
		Subject:       "with attachment",
		Text:          "see attached",
		AttachmentIDs: []string{att.ID},
	}, sender)
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if !bytes.Contains(sender.raw, []byte("Content-Disposition: attachment; filename=\"notes.txt\"")) {
		t.Fatalf("sent MIME did not contain attachment: %s", string(sender.raw))
	}
	atts, err := s.ListAttachments(ctx, uid, msg.ID)
	if err != nil {
		t.Fatalf("list attachments: %v", err)
	}
	if len(atts) != 1 || atts[0].ID != att.ID || atts[0].MessageID != msg.ID {
		t.Fatalf("attachments = %+v, want linked %s", atts, msg.ID)
	}
}

type stubSender struct{}

func (stubSender) Name() string                                      { return "stub" }
func (stubSender) Send(ctx context.Context, o letter.Outgoing) error { return nil }

type captureSender struct {
	raw []byte
}

func (s *captureSender) Name() string { return "capture" }
func (s *captureSender) Send(ctx context.Context, o letter.Outgoing) error {
	raw, err := letter.BuildRFC822(o)
	if err != nil {
		return err
	}
	s.raw = raw
	return nil
}

// TestSearch verifies the q= filter matches subject, from and body and that
// LIKE wildcards in the search string are escaped.
func TestSearch(t *testing.T) {
	s, ctx := newService(t)
	const uid = "u1"
	mustCreateMailbox(t, s, ctx, uid, "alice@airbrew.local")

	mustIngest(t, s, ctx, "alice@airbrew.local", []byte(
		"From: bob@ext.com\r\nTo: alice@airbrew.local\r\n"+
			"Message-ID: <m-s1>\r\nSubject: quarterly 50% report\r\n"+
			"Content-Type: text/plain; charset=utf-8\r\n\r\nlaunch blockers"))
	mustIngest(t, s, ctx, "alice@airbrew.local", []byte(
		"From: carol@ext.com\r\nTo: alice@airbrew.local\r\n"+
			"Message-ID: <m-s2>\r\nSubject: lunch?\r\n"+
			"Content-Type: text/plain; charset=utf-8\r\n\r\nlet us eat at 50% off"))

	for _, tc := range []struct {
		name string
		q    string
		want int
	}{
		{"matches subject word", "quarterly", 1},
		{"matches from", "carol", 1},
		{"matches body", "blockers", 1},
		{"literal percent is not a wildcard", "50%", 2},
		{"no hit", "missing-term", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := s.ListMessages(ctx, inbox.ListFilter{UserID: uid, Query: tc.q})
			if err != nil {
				t.Fatalf("ListMessages: %v", err)
			}
			if len(got) != tc.want {
				t.Fatalf("got %d, want %d", len(got), tc.want)
			}
		})
	}
}

// TestCounts checks per-folder totals after a representative mix of messages.
func TestCounts(t *testing.T) {
	s, ctx := newService(t)
	const uid = "u1"
	mb := mustCreateMailbox(t, s, ctx, uid, "alice@airbrew.local")

	mustIngest(t, s, ctx, "alice@airbrew.local", rawMsg("c1", "", "alice@airbrew.local"))
	mustIngest(t, s, ctx, "alice@airbrew.local", rawMsg("c2", "", "alice@airbrew.local"))
	if _, err := s.Send(ctx, uid, inbox.SendInput{
		MailboxID: mb.ID, To: []letter.Address{{Address: "x@ext.com"}},
		Subject: "out", Text: "going",
	}, stubSender{}); err != nil {
		t.Fatalf("send: %v", err)
	}
	if _, err := s.SaveDraft(ctx, uid, "", inbox.SendInput{
		MailboxID: mb.ID, To: []letter.Address{{Address: "x@ext.com"}},
		Subject: "draft", Text: "wip",
	}); err != nil {
		t.Fatalf("draft: %v", err)
	}
	starID := func() string {
		msgs, _ := s.ListMessages(ctx, inbox.ListFilter{UserID: uid})
		return msgs[0].ID
	}()
	if err := s.PatchFlags(ctx, uid, starID, inbox.FlagPatch{IsStarred: boolPtr(true)}); err != nil {
		t.Fatalf("star: %v", err)
	}

	c, err := s.Counts(ctx, uid, "")
	if err != nil {
		t.Fatalf("Counts: %v", err)
	}
	if c.Inbox != 2 || c.Sent != 1 || c.Draft != 1 || c.Starred != 1 || c.Unread != 2 {
		t.Fatalf("counts = %+v, want inbox=2 sent=1 draft=1 starred=1 unread=2", c)
	}
}

// TestDraftSaveUpdateSend covers the draft lifecycle: create, edit, then send.
func TestDraftSaveUpdateSend(t *testing.T) {
	s, ctx := newService(t)
	const uid = "u1"
	mb := mustCreateMailbox(t, s, ctx, uid, "alice@airbrew.local")

	draft, err := s.SaveDraft(ctx, uid, "", inbox.SendInput{
		MailboxID: mb.ID, To: []letter.Address{{Address: "bob@ext.com"}},
		Subject:   "first", Text: "v1",
	})
	if err != nil {
		t.Fatalf("save draft: %v", err)
	}
	if !draft.IsDraft || draft.Direction != inbox.DirectionOutbound {
		t.Fatalf("draft stored wrong: %+v", draft)
	}
	updated, err := s.SaveDraft(ctx, uid, draft.ID, inbox.SendInput{
		MailboxID: mb.ID, To: []letter.Address{{Address: "bob@ext.com"}},
		Subject:   "second", Text: "v2",
	})
	if err != nil {
		t.Fatalf("update draft: %v", err)
	}
	if updated.Subject != "second" || updated.BodyText != "v2" || updated.ID != draft.ID {
		t.Fatalf("update did not stick: %+v", updated)
	}
	sent, err := s.SendDraft(ctx, uid, draft.ID, &captureSender{})
	if err != nil {
		t.Fatalf("send draft: %v", err)
	}
	if sent.IsDraft {
		t.Fatalf("sent draft still flagged as draft")
	}
	if sent.SentAt == nil {
		t.Fatalf("sent_at missing")
	}
}

// TestDraftReplyJoinsParentThread verifies a reply draft is grouped under the
// parent thread both before send (when it has no Message-ID) and after send.
func TestDraftReplyJoinsParentThread(t *testing.T) {
	s, ctx := newService(t)
	const uid = "u1"
	mb := mustCreateMailbox(t, s, ctx, uid, "alice@airbrew.local")
	mustIngest(t, s, ctx, "alice@airbrew.local", rawMsg("parent-1", "", "alice@airbrew.local"))

	draft, err := s.SaveDraft(ctx, uid, "", inbox.SendInput{
		MailboxID:  mb.ID,
		To:         []letter.Address{{Address: "bob@ext.com"}},
		Subject:    "Re: test",
		Text:       "draft reply",
		InReplyTo:  "parent-1",
		References: []string{"parent-1"},
	})
	if err != nil {
		t.Fatalf("save draft: %v", err)
	}
	if draft.ThreadID != "parent-1" {
		t.Fatalf("draft thread_id = %q, want parent-1", draft.ThreadID)
	}
	sent, err := s.SendDraft(ctx, uid, draft.ID, &captureSender{})
	if err != nil {
		t.Fatalf("send draft: %v", err)
	}
	if sent.ThreadID != "parent-1" {
		t.Fatalf("sent thread_id = %q, want parent-1", sent.ThreadID)
	}
	threads, _ := s.ListThreads(ctx, uid, "", 10, 0)
	if len(threads) != 1 {
		t.Fatalf("expected single thread after draft+send, got %d", len(threads))
	}
}

func mustIngest(t *testing.T, s *inbox.Service, ctx context.Context, recipient string, raw []byte) {
	t.Helper()
	if _, err := s.Ingest(ctx, recipient, raw, time.Now()); err != nil {
		t.Fatalf("ingest: %v", err)
	}
}

func boolPtr(b bool) *bool { return &b }

// TestDeleteCleansAttachmentBlobs verifies that deleting a message also
// removes the blobs of every attachment bound to it, not only the row.
func TestDeleteCleansAttachmentBlobs(t *testing.T) {
	blobs, err := blob.NewLocal(t.TempDir())
	if err != nil {
		t.Fatalf("blob store: %v", err)
	}
	// Wrap to assert the blob path disappears.
	s, ctx := newServiceWithBlob(t, blobs)
	const uid = "u1"
	mustCreateMailbox(t, s, ctx, uid, "alice@airbrew.local")

	mustIngest(t, s, ctx, "alice@airbrew.local", []byte(
		"From: bob@ext.com\r\nTo: alice@airbrew.local\r\nMessage-ID: <m-att>\r\n"+
			"Subject: with attach\r\nMIME-Version: 1.0\r\n"+
			"Content-Type: multipart/mixed; boundary=\"b\"\r\n\r\n"+
			"--b\r\nContent-Type: text/plain\r\n\r\nhi\r\n"+
			"--b\r\nContent-Type: text/plain; name=\"a.txt\"\r\n"+
			"Content-Disposition: attachment; filename=\"a.txt\"\r\n\r\nxxx\r\n"+
			"--b--\r\n"))
	msgs, err := s.ListMessages(ctx, inbox.ListFilter{UserID: uid})
	if err != nil || len(msgs) != 1 {
		t.Fatalf("list: %v (%d)", err, len(msgs))
	}
	msg := msgs[0]
	atts, err := s.ListAttachments(ctx, uid, msg.ID)
	if err != nil || len(atts) != 1 {
		t.Fatalf("attachments: %v (%d)", err, len(atts))
	}
	blobPath := atts[0].BlobPath
	if blobPath == "" {
		t.Fatal("blob path empty")
	}
	if _, _, err := blobs.Open(ctx, blobPath); err != nil {
		t.Fatalf("blob should exist before delete: %v", err)
	}
	if err := s.DeleteMessage(ctx, uid, msg.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, _, err := blobs.Open(ctx, blobPath); err == nil {
		t.Fatal("blob should be gone after delete")
	}
}
