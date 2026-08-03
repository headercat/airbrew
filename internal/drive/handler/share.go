package handler

import (
	"errors"
	"io"
	"net/http"
	"strconv"

	"github.com/headercat/airbrew/internal/drive/files"
)

// RegisterShareRoutes mounts the public share endpoints on mux. These are
// reachable without a session.
func (h *Handler) RegisterShareRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/drive/s/{token}", h.getShare)
	mux.HandleFunc("GET /api/drive/s/{token}/download", h.downloadShare)
}

type publicShareResp struct {
	Name        string `json:"name"`
	ContentType string `json:"content_type"`
	SizeBytes   int64  `json:"size_bytes"`
	ExpiresAt   string `json:"expires_at,omitempty"`
}

// getShare returns public metadata for a share so a landing page can render
// without revealing the file contents. The password is read from the
// X-Share-Password header (preferred) or the legacy ?pw= query parameter.
func (h *Handler) getShare(w http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")
	pw := sharePassword(r)
	n, sh, err := h.svc.OpenShare(r.Context(), token, pw)
	if err != nil {
		writeShareErr(w, err)
		return
	}
	out := publicShareResp{
		Name: n.Name, ContentType: n.ContentType, SizeBytes: n.SizeBytes,
	}
	if sh.ExpiresAt != nil {
		out.ExpiresAt = sh.ExpiresAt.UTC().Format(timeRFC3339)
	}
	jsonResp(w, http.StatusOK, out)
}

// downloadShare streams the shared file. For password-protected shares the
// password is supplied via the X-Share-Password header (preferred) or the
// legacy ?pw= query parameter.
func (h *Handler) downloadShare(w http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")
	pw := sharePassword(r)
	n, _, err := h.svc.OpenShare(r.Context(), token, pw)
	if err != nil {
		writeShareErr(w, err)
		return
	}
	if n.BlobPath == "" || h.blobs == nil {
		respondErr(w, http.StatusNotFound, "not_found", "file content missing")
		return
	}
	body, ct, err := h.blobs.Open(r.Context(), n.BlobPath)
	if err != nil {
		respondErr(w, http.StatusNotFound, "not_found", "file content missing")
		return
	}
	defer body.Close()
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
	if _, err := io.Copy(w, body); err == nil {
		h.svc.IncDownload(r.Context(), token) // count only successful transfers
	}
}

// writeShareErr maps share-access errors with the codes a public caller needs.
func writeShareErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, files.ErrShareNotFound), errors.Is(err, files.ErrNotFound):
		respondErr(w, http.StatusNotFound, "not_found", "share not found or revoked")
	case errors.Is(err, files.ErrExpired):
		respondErr(w, http.StatusGone, "expired", "share link has expired")
	case errors.Is(err, files.ErrPasswordRequired):
		respondErr(w, http.StatusUnauthorized, "password_required", "password required")
	default:
		respondErr(w, http.StatusInternalServerError, "internal_error", err.Error())
	}
}

// sharePassword returns the share password from the X-Share-Password header.
// Query-string (?pw=) is intentionally not accepted: it would leak the
// password into access logs, browser history, and the Referer header.
func sharePassword(r *http.Request) string {
	return r.Header.Get("X-Share-Password")
}
