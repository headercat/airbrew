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
	"github.com/headercat/airbrew/internal/db"
	"github.com/headercat/airbrew/internal/drive/files"
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

type harness struct {
	svc   *files.Service
	h     *Handler
	mux   *http.ServeMux
	uid   string
	blobs *memStoreH
}

type memStoreH struct{ files map[string][]byte }

func (m *memStoreH) Save(_ context.Context, ns, _ string, r io.Reader) (string, error) {
	b, err := io.ReadAll(r)
	if err != nil {
		return "", err
	}
	p := ns + "/" + id.New() + ".bin"
	m.files[p] = b
	return p, nil
}
func (m *memStoreH) Open(_ context.Context, p string) (io.ReadCloser, string, error) {
	b, ok := m.files[p]
	if !ok {
		return nil, "", io.EOF
	}
	return io.NopCloser(bytes.NewReader(b)), "application/octet-stream", nil
}
func (m *memStoreH) Delete(_ context.Context, p string) error { delete(m.files, p); return nil }
func (m *memStoreH) List(_ context.Context, ns string) ([]string, error) {
	var out []string
	for k := range m.files {
		if strings.HasPrefix(k, ns+"/") {
			out = append(out, k)
		}
	}
	return out, nil
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "drive-handler.db"))
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
	repo := files.NewRepository(d.DB)
	store := &memStoreH{files: map[string][]byte{}}
	svc := files.NewService(repo, store, files.Config{MaxUploadBytes: 1 << 20, QuotaBytes: 1 << 20})
	h := New(svc, store, audit.NewService(d.DB))
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	h.RegisterShareRoutes(mux)
	return &harness{svc: svc, h: h, mux: mux, uid: uid, blobs: store}
}

func (hw *harness) do(t *testing.T, method, target, body, ct string) *httptest.ResponseRecorder {
	t.Helper()
	var r io.Reader
	if body != "" {
		r = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, target, r)
	if ct != "" {
		req.Header.Set("Content-Type", ct)
	}
	rec := httptest.NewRecorder()
	withSession(hw.uid, hw.mux).ServeHTTP(rec, req)
	return rec
}

func (hw *harness) doNoSession(method, target, pw string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, nil)
	if pw != "" {
		req.Header.Set("X-Share-Password", pw)
	}
	rec := httptest.NewRecorder()
	hw.mux.ServeHTTP(rec, req)
	return rec
}

func (hw *harness) createFolder(t *testing.T, name string) *files.Node {
	t.Helper()
	rec := hw.do(t, "POST", "/api/drive/folders", `{"name":"`+name+`"}`, "application/json")
	if rec.Code != http.StatusCreated {
		t.Fatalf("create folder %q: %d %s", name, rec.Code, rec.Body.String())
	}
	var resp struct{ ID string }
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	n, _ := hw.svc.Get(context.Background(), hw.uid, resp.ID)
	return n
}

func TestRequiresSession(t *testing.T) {
	hw := newHarness(t)
	if rec := hw.doNoSession("GET", "/api/drive/files", ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 without session, got %d", rec.Code)
	}
}

func TestUploadListDownload(t *testing.T) {
	hw := newHarness(t)
	body := &bytes.Buffer{}
	mw := multipart.NewWriter(body)
	fw, _ := mw.CreateFormFile("file", "hello.txt")
	_, _ = fw.Write([]byte("hello world"))
	mw.Close()
	rec := hw.do(t, "POST", "/api/drive/files", body.String(), mw.FormDataContentType())
	if rec.Code != http.StatusCreated {
		t.Fatalf("upload: %d %s", rec.Code, rec.Body.String())
	}
}

func TestPatchRenameAndStar(t *testing.T) {
	hw := newHarness(t)
	n := hw.createFolder(t, "orig")
	if rec := hw.do(t, "PATCH", "/api/drive/files/"+n.ID, `{"name":"renamed"}`, "application/json"); rec.Code != http.StatusOK {
		t.Fatalf("rename: %d %s", rec.Code, rec.Body.String())
	}
	if rec := hw.do(t, "PATCH", "/api/drive/files/"+n.ID, `{"starred":true}`, "application/json"); rec.Code != http.StatusOK {
		t.Fatalf("star: %d %s", rec.Code, rec.Body.String())
	}
	if rec := hw.do(t, "PATCH", "/api/drive/files/"+n.ID, `{}`, "application/json"); rec.Code != http.StatusBadRequest {
		t.Fatalf("empty patch: expected 400, got %d", rec.Code)
	}
}

func TestTrashRestoreDelete(t *testing.T) {
	hw := newHarness(t)
	n := hw.createFolder(t, "tmp")
	if rec := hw.do(t, "DELETE", "/api/drive/files/"+n.ID, "", ""); rec.Code != http.StatusOK {
		t.Fatalf("trash: %d %s", rec.Code, rec.Body.String())
	}
	if rec := hw.do(t, "POST", "/api/drive/files/"+n.ID+"/restore", "", ""); rec.Code != http.StatusOK {
		t.Fatalf("restore: %d %s", rec.Code, rec.Body.String())
	}
	if rec := hw.do(t, "DELETE", "/api/drive/files/"+n.ID+"?permanent=true", "", ""); rec.Code != http.StatusOK {
		t.Fatalf("permanent delete: %d %s", rec.Code, rec.Body.String())
	}
	if rec := hw.do(t, "GET", "/api/drive/files/"+n.ID, "", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("get deleted: expected 404, got %d", rec.Code)
	}
}

func TestMoveCircularRejected(t *testing.T) {
	hw := newHarness(t)
	a := hw.createFolder(t, "A")
	b, err := hw.svc.CreateFolder(context.Background(), files.CreateFolderInput{UserID: hw.uid, ParentID: a.ID, Name: "B"})
	if err != nil {
		t.Fatal(err)
	}
	rec := hw.do(t, "PATCH", "/api/drive/files/"+a.ID, `{"parent_id":"`+b.ID+`"}`, "application/json")
	if rec.Code != http.StatusConflict {
		t.Fatalf("circular move: expected 409, got %d %s", rec.Code, rec.Body.String())
	}
}

func TestSharePublicAccess(t *testing.T) {
	hw := newHarness(t)
	body := &bytes.Buffer{}
	mw := multipart.NewWriter(body)
	fw, _ := mw.CreateFormFile("file", "s.txt")
	_, _ = fw.Write([]byte("shared"))
	mw.Close()
	up := hw.do(t, "POST", "/api/drive/files", body.String(), mw.FormDataContentType())
	var file struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(up.Body.Bytes(), &file)

	rec := hw.do(t, "POST", "/api/drive/files/"+file.ID+"/shares", `{}`, "application/json")
	if rec.Code != http.StatusCreated {
		t.Fatalf("create share: %d %s", rec.Code, rec.Body.String())
	}
	var sh struct {
		Token string `json:"token"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &sh)

	if rec := hw.doNoSession("GET", "/api/drive/s/"+sh.Token, ""); rec.Code != http.StatusOK {
		t.Fatalf("public meta: %d %s", rec.Code, rec.Body.String())
	}
	if rec := hw.doNoSession("GET", "/api/drive/s/"+sh.Token+"/download", ""); rec.Code != http.StatusOK || rec.Body.String() != "shared" {
		t.Fatalf("public download: %d %q", rec.Code, rec.Body.String())
	}
}

func TestSharePasswordGate(t *testing.T) {
	hw := newHarness(t)
	body := &bytes.Buffer{}
	mw := multipart.NewWriter(body)
	fw, _ := mw.CreateFormFile("file", "p.txt")
	_, _ = fw.Write([]byte("secret"))
	mw.Close()
	up := hw.do(t, "POST", "/api/drive/files", body.String(), mw.FormDataContentType())
	var file struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(up.Body.Bytes(), &file)

	rec := hw.do(t, "POST", "/api/drive/files/"+file.ID+"/shares", `{"password":"hunter2"}`, "application/json")
	var sh struct {
		Token string `json:"token"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &sh)

	if rec := hw.doNoSession("GET", "/api/drive/s/"+sh.Token, ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("no pw: expected 401, got %d", rec.Code)
	}
	if rec := hw.doNoSession("GET", "/api/drive/s/"+sh.Token, "nope"); rec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong pw: expected 401, got %d", rec.Code)
	}
	if rec := hw.doNoSession("GET", "/api/drive/s/"+sh.Token, "hunter2"); rec.Code != http.StatusOK {
		t.Fatalf("correct pw: expected 200, got %d", rec.Code)
	}
}

func TestErrorMapping(t *testing.T) {
	hw := newHarness(t)
	if rec := hw.do(t, "GET", "/api/drive/files/does-not-exist", "", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("missing node: expected 404, got %d", rec.Code)
	}
}
