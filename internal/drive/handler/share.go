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
	HasPassword bool   `json:"has_password"`
	Expired     bool   `json:"expired"`
}

// getShare returns public metadata for a share so a landing page can render
// without revealing the file contents.
func (h *Handler) getShare(w http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")
	pw := r.URL.Query().Get("pw")
	n, err := h.svc.OpenShare(r.Context(), token, pw)
	if err != nil {
		writeShareErr(w, err)
		return
	}
	jsonResp(w, http.StatusOK, publicShareResp{
		Name:        n.Name,
		ContentType: n.ContentType,
		SizeBytes:   n.SizeBytes,
	})
}

// downloadShare streams the shared file. For password-protected shares the
// password is supplied via the "pw" query parameter or X-Share-Password header.
func (h *Handler) downloadShare(w http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")
	pw := r.URL.Query().Get("pw")
	if pw == "" {
		pw = r.Header.Get("X-Share-Password")
	}
	n, err := h.svc.OpenShare(r.Context(), token, pw)
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
	if n.SizeBytes > 0 {
		w.Header().Set("Content-Length", strconv.FormatInt(n.SizeBytes, 10))
	}
	h.svc.IncDownload(r.Context(), token)
	_, _ = io.Copy(w, body)
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
