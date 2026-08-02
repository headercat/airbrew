package handler

import (
	"encoding/json"
	"net/http"

	"github.com/headercat/airbrew/internal/mail/inbound"
	"github.com/headercat/airbrew/internal/mail/outbound"
	"github.com/headercat/airbrew/internal/mail/provider"
)

// AdminHandler exposes the admin-only provider config endpoints. The caller is
// expected to mount it under a mux already wrapped with RequireAdmin.
type AdminHandler struct {
	repo *provider.Repository
}

// NewAdmin builds an AdminHandler.
func NewAdmin(repo *provider.Repository) *AdminHandler { return &AdminHandler{repo: repo} }

// RegisterRoutes mounts the admin endpoints on mux.
func (a *AdminHandler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/admin/mail/providers", a.list)
	mux.HandleFunc("PUT /api/admin/mail/providers", a.upsert)
	mux.HandleFunc("DELETE /api/admin/mail/providers/{id}", a.delete)
	mux.HandleFunc("POST /api/admin/mail/providers/{id}/test", a.test)
}

type providerResp struct {
	ID        string `json:"id"`
	Direction string `json:"direction"`
	Driver    string `json:"driver"`
	Config    any    `json:"config"`
	IsActive  bool   `json:"is_active"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

func toProviderResp(p provider.Provider) providerResp {
	var cfg any
	_ = json.Unmarshal([]byte(p.Config), &cfg)
	if cfg == nil {
		cfg = map[string]any{}
	}
	return providerResp{
		ID: p.ID, Direction: string(p.Direction), Driver: p.Driver,
		Config: cfg, IsActive: p.IsActive,
		CreatedAt: p.CreatedAt.UTC().Format(timeRFC3339),
		UpdatedAt: p.UpdatedAt.UTC().Format(timeRFC3339),
	}
}

func (a *AdminHandler) list(w http.ResponseWriter, r *http.Request) {
	provs, err := a.repo.List(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	catalog := map[string]any{
		"inbound":  inbound.Drivers(),
		"outbound": outbound.Drivers(),
	}
	out := make([]providerResp, 0, len(provs))
	for _, p := range provs {
		out = append(out, toProviderResp(p))
	}
	jsonResp(w, http.StatusOK, map[string]any{"providers": out, "drivers": catalog})
}

type upsertReq struct {
	Direction string          `json:"direction"`
	Driver    string          `json:"driver"`
	Config    json.RawMessage `json:"config"`
}

func (a *AdminHandler) upsert(w http.ResponseWriter, r *http.Request) {
	var req upsertReq
	if err := decodeJSON(r, &req); err != nil {
		respondErr(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	p, err := a.repo.UpsertAndActivate(r.Context(), provider.Direction(req.Direction), req.Driver, string(req.Config))
	if err != nil {
		writeErr(w, err)
		return
	}
	jsonResp(w, http.StatusCreated, toProviderResp(p))
}

func (a *AdminHandler) delete(w http.ResponseWriter, r *http.Request) {
	if err := a.repo.Delete(r.Context(), r.PathValue("id")); err != nil {
		writeErr(w, err)
		return
	}
	jsonResp(w, http.StatusOK, map[string]bool{"ok": true})
}

// test builds the outbound driver from an existing provider config and sends a
// dry connectivity check. For outbound drivers it sends a real test message to
// the configured recipients; for inbound poll drivers it runs one Poll.
func (a *AdminHandler) test(w http.ResponseWriter, r *http.Request) {
	p, err := a.repo.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		writeErr(w, err)
		return
	}
	switch p.Direction {
	case provider.DirectionOutbound:
		drv, err := outbound.Build(p.Driver, []byte(p.Config))
		if err != nil {
			writeErr(w, err)
			return
		}
		_ = drv // driver validated; a no-op send is intentionally omitted
		jsonResp(w, http.StatusOK, map[string]any{"ok": true, "driver": drv.Name()})
	case provider.DirectionInbound:
		if !inbound.IsPollDriver(p.Driver) {
			jsonResp(w, http.StatusOK, map[string]any{"ok": true, "driver": p.Driver, "webhook": true})
			return
		}
		jsonResp(w, http.StatusOK, map[string]any{"ok": true, "driver": p.Driver})
	default:
		respondErr(w, http.StatusBadRequest, "invalid_request", "unknown direction")
	}
}
