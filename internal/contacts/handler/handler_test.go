package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/headercat/airbrew/internal/audit"
	"github.com/headercat/airbrew/internal/auth/session"
	"github.com/headercat/airbrew/internal/contacts/contact"
	"github.com/headercat/airbrew/internal/db"
	"github.com/headercat/airbrew/internal/id"
)

// withSession wraps next so every request runs with a fixed test session,
// mirroring what SessionMiddleware does in production.
func withSession(uid string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r = r.WithContext(session.WithContext(r.Context(), &session.Session{ID: "sess", UserID: uid}))
		next.ServeHTTP(w, r)
	})
}

type memStore struct{ files map[string][]byte }

func (m *memStore) Save(_ context.Context, ns, ct string, r io.Reader) (string, error) {
	b, err := io.ReadAll(r)
	if err != nil {
		return "", err
	}
	p := ns + "/" + id.New()
	m.files[p] = b
	_ = ct
	return p, nil
}
func (m *memStore) Open(_ context.Context, p string) (io.ReadCloser, string, error) {
	b, ok := m.files[p]
	if !ok {
		return nil, "", io.EOF
	}
	return io.NopCloser(bytes.NewReader(b)), "image/png", nil
}
func (m *memStore) Delete(_ context.Context, p string) error { delete(m.files, p); return nil }
func (m *memStore) List(_ context.Context, ns string) ([]string, error) {
	var out []string
	for k := range m.files {
		if strings.HasPrefix(k, ns+"/") {
			out = append(out, k)
		}
	}
	return out, nil
}

type harness struct {
	svc   *contact.Service
	h     *Handler
	mux   *http.ServeMux
	uid   string
	blobs *memStore
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "contacts-handler.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	if err := d.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	uid := "u_" + id.New()
	if _, err := d.DB.ExecContext(context.Background(),
		`INSERT INTO users (id, email, public_subject, status) VALUES (?, ?, ?, 'active')`,
		uid, uid+"@example.com", uid); err != nil {
		t.Fatal(err)
	}
	blobs := &memStore{files: map[string][]byte{}}
	svc := contact.NewService(contact.NewRepository(d.DB), blobs)
	h := New(svc, blobs, audit.NewService(d.DB))
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	return &harness{svc: svc, h: h, mux: mux, uid: uid, blobs: blobs}
}

func (hs *harness) do(t *testing.T, method, target string, body io.Reader, ct string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, target, body)
	if ct != "" {
		req.Header.Set("Content-Type", ct)
	}
	rec := httptest.NewRecorder()
	withSession(hs.uid, hs.mux).ServeHTTP(rec, req)
	return rec
}

func (hs *harness) doNoSession(method, target string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, nil)
	rec := httptest.NewRecorder()
	hs.mux.ServeHTTP(rec, req)
	return rec
}

func (hs *harness) createContact(t *testing.T, name string) string {
	t.Helper()
	body := bytes.NewReader([]byte(`{"display_name":"` + name + `","emails":[{"value":"` + name + `@example.com"}]}`))
	rec := hs.do(t, http.MethodPost, "/api/contacts", body, "application/json")
	if rec.Code != http.StatusCreated {
		t.Fatalf("create contact: %d %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	return resp.ID
}

func TestRequiresSession(t *testing.T) {
	hs := newHarness(t)
	// No session cookie → every user endpoint should 401.
	rec := hs.doNoSession(http.MethodGet, "/api/contacts")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 without session, got %d", rec.Code)
	}
}

func TestContactLifecycleAndErrorMapping(t *testing.T) {
	hs := newHarness(t)
	// Create → get → patch → delete.
	id := hs.createContact(t, "Ada")
	rec := hs.do(t, http.MethodGet, "/api/contacts/"+id, nil, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("get: %d", rec.Code)
	}
	// 404 for a missing contact.
	rec = hs.do(t, http.MethodGet, "/api/contacts/does-not-exist", nil, "")
	if rec.Code != http.StatusNotFound {
		t.Errorf("missing contact: expected 404, got %d", rec.Code)
	}
	// Patch favorite.
	rec = hs.do(t, http.MethodPatch, "/api/contacts/"+id,
		bytes.NewReader([]byte(`{"is_favorite":true}`)), "application/json")
	if rec.Code != http.StatusOK {
		t.Fatalf("patch: %d %s", rec.Code, rec.Body.String())
	}
	// Invalid body on create → 400.
	rec = hs.do(t, http.MethodPost, "/api/contacts",
		bytes.NewReader([]byte(`{}`)), "application/json")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("blank contact: expected 400, got %d", rec.Code)
	}
	// Delete.
	rec = hs.do(t, http.MethodDelete, "/api/contacts/"+id, nil, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("delete: %d", rec.Code)
	}
	rec = hs.do(t, http.MethodGet, "/api/contacts/"+id, nil, "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("after delete expected 404, got %d", rec.Code)
	}
}

func TestListReturnsTotal(t *testing.T) {
	hs := newHarness(t)
	hs.createContact(t, "Ada")
	hs.createContact(t, "Grace")
	rec := hs.do(t, http.MethodGet, "/api/contacts", nil, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("list: %d", rec.Code)
	}
	var resp struct {
		Contacts []map[string]any `json:"contacts"`
		Total    int              `json:"total"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Total < 2 {
		t.Errorf("expected total >= 2, got %d", resp.Total)
	}
}

// A 1x1 transparent PNG — its leading bytes make http.DetectContentType
// classify it as image/png.
var pngBytes = []byte{
	0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A, 0x00, 0x00, 0x00, 0x0D, 0x49,
	0x48, 0x44, 0x52, 0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01, 0x08, 0x06,
	0x00, 0x00, 0x00, 0x1F, 0x15, 0xC4, 0x89,
}

func TestAvatarUploadRejectsNonImage(t *testing.T) {
	hs := newHarness(t)
	id := hs.createContact(t, "Ada")
	// An SVG body sniffs as image/svg+xml (or text/xml), which is not on the
	// whitelist; expect 415 regardless of the declared Content-Type.
	svg := bytes.NewReader([]byte(`<svg xmlns="http://www.w3.org/2000/svg"></svg>`))
	rec := hs.do(t, http.MethodPost, "/api/contacts/avatars/"+id, svg, "image/png")
	if rec.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("expected 415 for SVG-as-png, got %d %s", rec.Code, rec.Body.String())
	}
	// Empty body → sniff yields "" → whitelist rejects.
	rec = hs.do(t, http.MethodPost, "/api/contacts/avatars/"+id,
		bytes.NewReader(nil), "image/png")
	if rec.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("expected 415 for empty avatar, got %d", rec.Code)
	}
}

func TestAvatarUploadAndFetch(t *testing.T) {
	hs := newHarness(t)
	id := hs.createContact(t, "Ada")

	// Multipart upload of a real PNG.
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, err := mw.CreateFormFile("file", "avatar.png")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fw.Write(pngBytes); err != nil {
		t.Fatal(err)
	}
	mw.Close()
	rec := hs.do(t, http.MethodPost, "/api/contacts/avatars/"+id, &buf, mw.FormDataContentType())
	if rec.Code != http.StatusOK {
		t.Fatalf("avatar upload: %d %s", rec.Code, rec.Body.String())
	}

	// GET avatar serves the bytes behind the session.
	rec = hs.do(t, http.MethodGet, "/api/contacts/avatars/"+id, nil, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("avatar fetch: %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "image/") {
		t.Errorf("avatar content type: got %q, want image/*", ct)
	}
	if v := rec.Header().Get("X-Content-Type-Options"); v != "nosniff" {
		t.Errorf("expected nosniff header, got %q", v)
	}

	// Avatar URL in the contact response points at the gated endpoint.
	rec = hs.do(t, http.MethodGet, "/api/contacts/"+id, nil, "")
	var c map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &c)
	if url, _ := c["avatar_url"].(string); !strings.Contains(url, "/api/contacts/avatars/"+id) {
		t.Errorf("avatar_url = %q, want gated endpoint", url)
	}
}

func TestImportVCardsReportsCreatedUpdatedFailed(t *testing.T) {
	hs := newHarness(t)
	vcf := "BEGIN:VCARD\r\nVERSION:4.0\r\nFN:Ada\r\nN:;;;\r\nUID:dup-1\r\nEND:VCARD\r\n" +
		"BEGIN:VCARD\r\nVERSION:4.0\r\nFN:Grace\r\nN:;;;\r\nUID:dup-2\r\nEND:VCARD\r\n"
	rec := hs.do(t, http.MethodPost, "/api/contacts/import",
		bytes.NewReader([]byte(vcf)), "text/vcard")
	if rec.Code != http.StatusCreated {
		t.Fatalf("import: %d %s", rec.Code, rec.Body.String())
	}
	var resp map[string]int
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp["created"] != 2 || resp["imported"] != 2 {
		t.Errorf("first import counts: %#v", resp)
	}
	// Re-import same UIDs → should update in place, no new contacts.
	rec = hs.do(t, http.MethodPost, "/api/contacts/import",
		bytes.NewReader([]byte(vcf)), "text/vcard")
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp["created"] != 0 || resp["updated"] != 2 {
		t.Errorf("re-import counts: %#v", resp)
	}
}
