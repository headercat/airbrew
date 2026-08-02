package handler

import (
	"net/http"

	"github.com/headercat/airbrew/internal/audit"
	"github.com/headercat/airbrew/internal/auth/session"
	"github.com/headercat/airbrew/internal/drive/files"
)

// AdminHandler exposes the admin-only drive endpoints (storage limits).
type AdminHandler struct {
	svc   *files.Service
	repo  *files.Repository
	audit *audit.Service
}

// NewAdmin builds an AdminHandler.
func NewAdmin(svc *files.Service, repo *files.Repository, auditSvc *audit.Service) *AdminHandler {
	return &AdminHandler{svc: svc, repo: repo, audit: auditSvc}
}

// RegisterRoutes mounts the admin endpoints on mux.
func (a *AdminHandler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/admin/drive/config", a.getConfig)
	mux.HandleFunc("PUT /api/admin/drive/config", a.putConfig)
}

type configResp struct {
	MaxUploadBytes int64 `json:"max_upload_bytes"`
	QuotaBytes     int64 `json:"quota_bytes"`
}

func (a *AdminHandler) getConfig(w http.ResponseWriter, r *http.Request) {
	c := a.svc.Config()
	jsonResp(w, http.StatusOK, configResp{
		MaxUploadBytes: c.MaxUploadBytes,
		QuotaBytes:     c.QuotaBytes,
	})
}

type putConfigReq struct {
	MaxUploadBytes *int64 `json:"max_upload_bytes"`
	QuotaBytes     *int64 `json:"quota_bytes"`
}

func (a *AdminHandler) putConfig(w http.ResponseWriter, r *http.Request) {
	var req putConfigReq
	if err := decodeJSON(r, &req); err != nil {
		respondErr(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	c := a.svc.Config()
	if req.MaxUploadBytes != nil {
		if *req.MaxUploadBytes < 0 {
			respondErr(w, http.StatusBadRequest, "invalid_request", "max_upload_bytes must be >= 0")
			return
		}
		c.MaxUploadBytes = *req.MaxUploadBytes
	}
	if req.QuotaBytes != nil {
		if *req.QuotaBytes < 0 {
			respondErr(w, http.StatusBadRequest, "invalid_request", "quota_bytes must be >= 0")
			return
		}
		c.QuotaBytes = *req.QuotaBytes
	}
	if err := a.repo.SetConfig(r.Context(), c); err != nil {
		respondErr(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	a.svc.SetConfig(c) // apply live
	if sess, ok := session.FromContext(r.Context()); ok {
		a.audit.Log(r.Context(), audit.Entry{
			EventType: "drive.config_updated", ActorUserID: sess.UserID,
			IPAddress: clientIP(r), UserAgent: r.UserAgent(),
			Metadata: map[string]any{
				"max_upload_bytes": c.MaxUploadBytes, "quota_bytes": c.QuotaBytes,
			},
		})
	}
	jsonResp(w, http.StatusOK, configResp{
		MaxUploadBytes: c.MaxUploadBytes, QuotaBytes: c.QuotaBytes,
	})
}
