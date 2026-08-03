// Package handler exposes the HTTP endpoints for the drive module: file and
// folder management, uploads and downloads, the trash, and the share surface.
// User endpoints require a browser session (enforced by the caller wrapping the
// mux); the public share endpoints live in share.go.
package handler

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/headercat/airbrew/internal/audit"
	"github.com/headercat/airbrew/internal/blob"
	"github.com/headercat/airbrew/internal/drive/files"
)

// Handler exposes the drive JSON endpoints.
type Handler struct {
	svc   *files.Service
	blobs blob.Store
	audit *audit.Service
}

// New builds a Handler.
func New(svc *files.Service, blobs blob.Store, auditSvc *audit.Service) *Handler {
	return &Handler{svc: svc, blobs: blobs, audit: auditSvc}
}

// RegisterRoutes mounts the authenticated user endpoints on mux.
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/drive/files", h.listFiles)
	mux.HandleFunc("POST /api/drive/files", h.uploadFile)
	mux.HandleFunc("POST /api/drive/folders", h.createFolder)

	mux.HandleFunc("GET /api/drive/files/{id}", h.getFile)
	mux.HandleFunc("GET /api/drive/files/{id}/path", h.getPath)
	mux.HandleFunc("PATCH /api/drive/files/{id}", h.patchFile)
	mux.HandleFunc("DELETE /api/drive/files/{id}", h.deleteFile)
	mux.HandleFunc("POST /api/drive/files/{id}/restore", h.restoreFile)
	mux.HandleFunc("POST /api/drive/files/{id}/copy", h.copyFile)
	mux.HandleFunc("GET /api/drive/files/{id}/download", h.downloadFile)

	mux.HandleFunc("POST /api/drive/trash/empty", h.emptyTrash)
	mux.HandleFunc("GET /api/drive/usage", h.usage)

	mux.HandleFunc("GET /api/drive/shares", h.listShares)
	mux.HandleFunc("GET /api/drive/files/{id}/shares", h.listNodeShares)
	mux.HandleFunc("POST /api/drive/files/{id}/shares", h.createShare)
	mux.HandleFunc("DELETE /api/drive/shares/{id}", h.deleteShare)
}

// --- responses -------------------------------------------------------------

type nodeResp struct {
	ID          string `json:"id"`
	ParentID    string `json:"parent_id"`
	Kind        string `json:"kind"`
	Name        string `json:"name"`
	ContentType string `json:"content_type"`
	SizeBytes   int64  `json:"size_bytes"`
	SHA256      string `json:"sha256,omitempty"`
	IsStarred   bool   `json:"is_starred"`
	DeletedAt   string `json:"deleted_at,omitempty"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
}

func toNodeResp(n *files.Node) nodeResp {
	out := nodeResp{
		ID: n.ID, ParentID: n.ParentID, Kind: string(n.Kind), Name: n.Name,
		ContentType: n.ContentType, SizeBytes: n.SizeBytes, SHA256: n.SHA256,
		IsStarred: n.IsStarred,
		CreatedAt: n.CreatedAt.UTC().Format(timeRFC3339),
		UpdatedAt: n.UpdatedAt.UTC().Format(timeRFC3339),
	}
	if n.DeletedAt != nil {
		out.DeletedAt = n.DeletedAt.UTC().Format(timeRFC3339)
	}
	return out
}

type shareResp struct {
	ID          string `json:"id"`
	NodeID      string `json:"node_id"`
	NodeName    string `json:"node_name"`
	NodeTrashed bool   `json:"node_trashed"`
	Token       string `json:"token"`
	URL         string `json:"url"`
	HasPassword bool   `json:"has_password"`
	ExpiresAt   string `json:"expires_at,omitempty"`
	Downloads   int64  `json:"downloads"`
	IsActive    bool   `json:"is_active"`
	CreatedAt   string `json:"created_at"`
}

func toShareResp(s *files.Share) shareResp {
	out := shareResp{
		ID: s.ID, NodeID: s.NodeID, NodeName: s.NodeName, NodeTrashed: s.NodeTrashed,
		Token: s.Token, URL: "/s/" + s.Token, HasPassword: s.HasPassword,
		Downloads: s.Downloads, IsActive: s.IsActive,
		CreatedAt: s.CreatedAt.UTC().Format(timeRFC3339),
	}
	if s.ExpiresAt != nil {
		out.ExpiresAt = s.ExpiresAt.UTC().Format(timeRFC3339)
	}
	return out
}

// --- listing & folders -----------------------------------------------------

func (h *Handler) listFiles(w http.ResponseWriter, r *http.Request) {
	sess, ok := requireSession(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	folder := q.Get("folder")
	if folder == "" {
		switch q.Get("view") {
		case "starred":
			folder = "starred"
		case "trash":
			folder = "trash"
		case "search":
			folder = "search"
		}
	}
	f := files.ListFilter{
		UserID:   sess.UserID,
		ParentID: q.Get("parent"),
		Folder:   folder,
		Search:   q.Get("q"),
		Kind:     files.Kind(q.Get("kind")),
		SortBy:   q.Get("sort"),
		SortDesc: parseBool(q.Get("order")),
		Limit:    parseInt(q.Get("limit")),
		Offset:   parseInt(q.Get("offset")),
	}
	nodes, err := h.svc.List(r.Context(), f)
	if err != nil {
		writeErr(w, err)
		return
	}
	total, _ := h.svc.ListTotal(r.Context(), f)
	out := make([]nodeResp, 0, len(nodes))
	for _, n := range nodes {
		out = append(out, toNodeResp(n))
	}
	jsonResp(w, http.StatusOK, map[string]any{"nodes": out, "total": total})
}

type createFolderReq struct {
	ParentID string `json:"parent_id"`
	Name     string `json:"name"`
}

func (h *Handler) createFolder(w http.ResponseWriter, r *http.Request) {
	sess, ok := requireSession(w, r)
	if !ok {
		return
	}
	var req createFolderReq
	if err := decodeJSON(r, &req); err != nil {
		respondErr(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	n, err := h.svc.CreateFolder(r.Context(), files.CreateFolderInput{
		UserID: sess.UserID, ParentID: req.ParentID, Name: req.Name,
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	h.audit.Log(r.Context(), audit.Entry{
		EventType: "drive.folder_created", ActorUserID: sess.UserID,
		TargetType: "drive_node", TargetID: n.ID,
		IPAddress: clientIP(r), UserAgent: r.UserAgent(),
		Metadata: map[string]any{"name": n.Name},
	})
	jsonResp(w, http.StatusCreated, toNodeResp(n))
}

// --- upload ----------------------------------------------------------------

func (h *Handler) uploadFile(w http.ResponseWriter, r *http.Request) {
	sess, ok := requireSession(w, r)
	if !ok {
		return
	}
	parentID := r.URL.Query().Get("parent")

	// Cap the request body at the per-upload limit (plus slack for multipart
	// overhead) so an oversized upload is rejected early by the HTTP layer,
	// not only after the streaming counter trips.
	if max := h.svc.Config().MaxUploadBytes; max > 0 {
		r.Body = http.MaxBytesReader(w, r.Body, max+(1<<20))
	}

	var name, contentType string
	var body io.Reader

	if mt := r.Header.Get("Content-Type"); strings.HasPrefix(mt, "multipart/form-data") {
		// Honour the per-upload limit; when unlimited (>0 means a cap here too)
		// fall back to a generous 1 GiB so the parser doesn't reject big files
		// the service would otherwise accept. The service's streaming counter is
		// the real backstop.
		parseMax := int64(1 << 30)
		if max := h.svc.Config().MaxUploadBytes; max > 0 {
			parseMax = max + (1 << 20)
		}
		if err := r.ParseMultipartForm(parseMax); err != nil {
			respondErr(w, http.StatusBadRequest, "invalid_request", "could not parse multipart: "+err.Error())
			return
		}
		f, hdr, err := r.FormFile("file")
		if err != nil {
			respondErr(w, http.StatusBadRequest, "invalid_request", "missing 'file' field")
			return
		}
		defer f.Close()
		name = r.FormValue("name")
		if name == "" {
			name = hdr.Filename
		}
		body = f
	} else {
		// Raw body upload: name and content type come from the query string.
		name = r.URL.Query().Get("name")
		contentType = r.Header.Get("Content-Type")
		body = r.Body
	}

	name = strings.TrimSpace(name)
	if name == "" {
		respondErr(w, http.StatusBadRequest, "invalid_request", "file name required")
		return
	}
	body, contentType = sniffIfNeeded(body, contentType)

	n, err := h.svc.Upload(r.Context(), files.UploadInput{
		UserID: sess.UserID, ParentID: parentID, Name: name,
		ContentType: contentType, Content: body,
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	h.audit.Log(r.Context(), audit.Entry{
		EventType: "drive.file_uploaded", ActorUserID: sess.UserID,
		TargetType: "drive_node", TargetID: n.ID,
		IPAddress: clientIP(r), UserAgent: r.UserAgent(),
		Metadata: map[string]any{"name": n.Name, "size": n.SizeBytes},
	})
	jsonResp(w, http.StatusCreated, toNodeResp(n))
}

// sniffIfNeeded fills in a content type by sniffing the first 512 bytes when
// the declared type is missing or generic. It returns a reader that replays the
// sniffed bytes followed by the rest of the stream.
func sniffIfNeeded(r io.Reader, contentType string) (io.Reader, string) {
	ct := strings.TrimSpace(contentType)
	if ct != "" && ct != "application/octet-stream" && ct != "multipart/form-data" {
		return r, ct
	}
	buf := make([]byte, 512)
	n, _ := io.ReadFull(r, buf)
	if n == 0 {
		return r, ct
	}
	head := buf[:n]
	detected := http.DetectContentType(head)
	if ct == "" {
		ct = detected
	} else if ct == "application/octet-stream" && !strings.HasPrefix(detected, "application/octet-stream") {
		ct = detected
	}
	return io.MultiReader(bytes.NewReader(head), r), ct
}

// --- single-node ops -------------------------------------------------------

func (h *Handler) getFile(w http.ResponseWriter, r *http.Request) {
	sess, ok := requireSession(w, r)
	if !ok {
		return
	}
	n, err := h.svc.Get(r.Context(), sess.UserID, r.PathValue("id"))
	if err != nil {
		writeErr(w, err)
		return
	}
	jsonResp(w, http.StatusOK, toNodeResp(n))
}

func (h *Handler) getPath(w http.ResponseWriter, r *http.Request) {
	sess, ok := requireSession(w, r)
	if !ok {
		return
	}
	chain, err := h.svc.GetPath(r.Context(), sess.UserID, r.PathValue("id"))
	if err != nil {
		writeErr(w, err)
		return
	}
	out := make([]nodeResp, 0, len(chain))
	for _, n := range chain {
		out = append(out, toNodeResp(n))
	}
	jsonResp(w, http.StatusOK, map[string]any{"nodes": out})
}

type patchFileReq struct {
	Name     *string `json:"name"`
	ParentID *string `json:"parent_id"`
	Starred  *bool   `json:"starred"`
}

func (h *Handler) patchFile(w http.ResponseWriter, r *http.Request) {
	sess, ok := requireSession(w, r)
	if !ok {
		return
	}
	var req patchFileReq
	if err := decodeJSON(r, &req); err != nil {
		respondErr(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if req.Name == nil && req.ParentID == nil && req.Starred == nil {
		respondErr(w, http.StatusBadRequest, "invalid_request", "at least one of name, parent_id or starred is required")
		return
	}
	id := r.PathValue("id")
	ctx := r.Context()
	// Apply all present fields in one request (rename + move + star together).
	n, err := h.svc.Patch(ctx, sess.UserID, id, req.Name, req.ParentID, req.Starred)
	if err != nil {
		writeErr(w, err)
		return
	}
	h.audit.Log(ctx, audit.Entry{
		EventType: patchEvent(req), ActorUserID: sess.UserID,
		TargetType: "drive_node", TargetID: id,
		IPAddress: clientIP(r), UserAgent: r.UserAgent(),
	})
	jsonResp(w, http.StatusOK, toNodeResp(n))
}

// patchEvent picks an audit event type for a patch, preferring the most
// significant change.
func patchEvent(req patchFileReq) string {
	switch {
	case req.Name != nil:
		return "drive.file_renamed"
	case req.ParentID != nil:
		return "drive.file_moved"
	case req.Starred != nil:
		return "drive.file_starred"
	default:
		return "drive.file_viewed"
	}
}

func (h *Handler) deleteFile(w http.ResponseWriter, r *http.Request) {
	sess, ok := requireSession(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	if parseBool(r.URL.Query().Get("permanent")) {
		if err := h.svc.DeletePermanent(r.Context(), sess.UserID, id); err != nil {
			writeErr(w, err)
			return
		}
		h.audit.Log(r.Context(), audit.Entry{
			EventType: "drive.file_deleted", ActorUserID: sess.UserID,
			TargetType: "drive_node", TargetID: id,
			IPAddress: clientIP(r), UserAgent: r.UserAgent(),
		})
	} else {
		if err := h.svc.Trash(r.Context(), sess.UserID, id); err != nil {
			writeErr(w, err)
			return
		}
		h.audit.Log(r.Context(), audit.Entry{
			EventType: "drive.file_trashed", ActorUserID: sess.UserID,
			TargetType: "drive_node", TargetID: id,
			IPAddress: clientIP(r), UserAgent: r.UserAgent(),
		})
	}
	jsonResp(w, http.StatusOK, map[string]bool{"ok": true})
}

func (h *Handler) restoreFile(w http.ResponseWriter, r *http.Request) {
	sess, ok := requireSession(w, r)
	if !ok {
		return
	}
	n, err := h.svc.Restore(r.Context(), sess.UserID, r.PathValue("id"))
	if err != nil {
		writeErr(w, err)
		return
	}
	h.audit.Log(r.Context(), audit.Entry{
		EventType: "drive.file_restored", ActorUserID: sess.UserID,
		TargetType: "drive_node", TargetID: r.PathValue("id"),
		IPAddress: clientIP(r), UserAgent: r.UserAgent(),
	})
	jsonResp(w, http.StatusOK, toNodeResp(n))
}

type copyFileReq struct {
	ParentID string `json:"parent_id"`
	Name     string `json:"name"`
}

func (h *Handler) copyFile(w http.ResponseWriter, r *http.Request) {
	sess, ok := requireSession(w, r)
	if !ok {
		return
	}
	var req copyFileReq
	if err := decodeJSON(r, &req); err != nil {
		respondErr(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	n, err := h.svc.Copy(r.Context(), sess.UserID, r.PathValue("id"), req.ParentID, req.Name)
	if err != nil {
		writeErr(w, err)
		return
	}
	h.audit.Log(r.Context(), audit.Entry{
		EventType: "drive.file_copied", ActorUserID: sess.UserID,
		TargetType: "drive_node", TargetID: r.PathValue("id"),
		IPAddress: clientIP(r), UserAgent: r.UserAgent(),
		Metadata: map[string]any{"copy_id": n.ID},
	})
	jsonResp(w, http.StatusCreated, toNodeResp(n))
}

func (h *Handler) downloadFile(w http.ResponseWriter, r *http.Request) {
	sess, ok := requireSession(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	meta, err := h.svc.Get(r.Context(), sess.UserID, id)
	if err != nil {
		writeErr(w, err)
		return
	}
	if meta.IsFolder() {
		body, n, err := h.svc.ArchiveFolder(r.Context(), sess.UserID, id)
		if err != nil {
			writeErr(w, err)
			return
		}
		defer body.Close()
		w.Header().Set("Content-Type", "application/zip")
		w.Header().Set("Content-Disposition", disposition(n.Name+".zip", false))
		w.Header().Set("Cache-Control", "private, no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if _, err := io.Copy(w, body); err != nil {
			slog.WarnContext(r.Context(), "drive: folder archive copy failed",
				"id", n.ID, "error", err)
		}
		return
	}
	body, n, err := h.svc.Download(r.Context(), sess.UserID, r.PathValue("id"))
	if err != nil {
		writeErr(w, err)
		return
	}
	defer body.Close()
	ct := n.ContentType
	if ct == "" {
		ct = "application/octet-stream"
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Content-Disposition", disposition(n.Name, parseBool(r.URL.Query().Get("inline"))))
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if n.SizeBytes > 0 {
		w.Header().Set("Content-Length", strconv.FormatInt(n.SizeBytes, 10))
	}
	if _, err := io.Copy(w, body); err != nil {
		slog.WarnContext(r.Context(), "drive: download copy failed",
			"id", n.ID, "error", err)
	}
}

// disposition builds a Content-Disposition header with both an ASCII filename
// fallback and a UTF-8 filename* (RFC 6266) so non-ASCII names survive Safari
// and strict download managers.
func disposition(name string, inline bool) string {
	if inline {
		return "inline"
	}
	clean := strings.NewReplacer("\"", "", "\n", "", "\r", "").Replace(name)
	if clean == "" {
		clean = "file"
	}
	ascii := strings.ToValidUTF8(clean, "_")
	return "attachment; filename=\"" + ascii + "\"; filename*=UTF-8''" + url.PathEscape(name)
}

func (h *Handler) emptyTrash(w http.ResponseWriter, r *http.Request) {
	sess, ok := requireSession(w, r)
	if !ok {
		return
	}
	if err := h.svc.EmptyTrash(r.Context(), sess.UserID); err != nil {
		writeErr(w, err)
		return
	}
	h.audit.Log(r.Context(), audit.Entry{
		EventType: "drive.trash_emptied", ActorUserID: sess.UserID,
		TargetType: "drive_node", IPAddress: clientIP(r), UserAgent: r.UserAgent(),
	})
	jsonResp(w, http.StatusOK, map[string]bool{"ok": true})
}

type usageResp struct {
	Used  int64 `json:"used"`
	Quota int64 `json:"quota"`
}

func (h *Handler) usage(w http.ResponseWriter, r *http.Request) {
	sess, ok := requireSession(w, r)
	if !ok {
		return
	}
	used, quota, err := h.svc.Usage(r.Context(), sess.UserID)
	if err != nil {
		writeErr(w, err)
		return
	}
	jsonResp(w, http.StatusOK, usageResp{Used: used, Quota: quota})
}

// --- shares ----------------------------------------------------------------

func (h *Handler) listShares(w http.ResponseWriter, r *http.Request) {
	sess, ok := requireSession(w, r)
	if !ok {
		return
	}
	shares, err := h.svc.ListShares(r.Context(), sess.UserID)
	if err != nil {
		writeErr(w, err)
		return
	}
	out := make([]shareResp, 0, len(shares))
	for _, s := range shares {
		out = append(out, toShareResp(s))
	}
	jsonResp(w, http.StatusOK, map[string]any{"shares": out})
}

func (h *Handler) listNodeShares(w http.ResponseWriter, r *http.Request) {
	sess, ok := requireSession(w, r)
	if !ok {
		return
	}
	shares, err := h.svc.ListSharesByNode(r.Context(), sess.UserID, r.PathValue("id"))
	if err != nil {
		writeErr(w, err)
		return
	}
	out := make([]shareResp, 0, len(shares))
	for _, s := range shares {
		out = append(out, toShareResp(s))
	}
	jsonResp(w, http.StatusOK, map[string]any{"shares": out})
}

type createShareReq struct {
	Password  string `json:"password"`
	ExpiresIn *int64 `json:"expires_in_seconds"` // optional, seconds from now
}

func (h *Handler) createShare(w http.ResponseWriter, r *http.Request) {
	sess, ok := requireSession(w, r)
	if !ok {
		return
	}
	var req createShareReq
	if err := decodeJSON(r, &req); err != nil {
		respondErr(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	var expires *time.Time
	if req.ExpiresIn != nil && *req.ExpiresIn > 0 {
		t := time.Now().UTC().Add(time.Duration(*req.ExpiresIn) * time.Second)
		expires = &t
	}
	s, err := h.svc.CreateShare(r.Context(), files.CreateShareInput{
		UserID: sess.UserID, NodeID: r.PathValue("id"),
		Password: req.Password, ExpiresAt: expires,
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	h.audit.Log(r.Context(), audit.Entry{
		EventType: "drive.share_created", ActorUserID: sess.UserID,
		TargetType: "drive_node", TargetID: r.PathValue("id"),
		IPAddress: clientIP(r), UserAgent: r.UserAgent(),
		Metadata: map[string]any{"share": s.ID, "password": req.Password != ""},
	})
	jsonResp(w, http.StatusCreated, toShareResp(s))
}

func (h *Handler) deleteShare(w http.ResponseWriter, r *http.Request) {
	sess, ok := requireSession(w, r)
	if !ok {
		return
	}
	if err := h.svc.DeleteShare(r.Context(), sess.UserID, r.PathValue("id")); err != nil {
		writeErr(w, err)
		return
	}
	h.audit.Log(r.Context(), audit.Entry{
		EventType: "drive.share_revoked", ActorUserID: sess.UserID,
		TargetType: "drive_share", TargetID: r.PathValue("id"),
		IPAddress: clientIP(r), UserAgent: r.UserAgent(),
	})
	jsonResp(w, http.StatusOK, map[string]bool{"ok": true})
}
