// Package handler exposes the HTTP endpoints for the password-vault module.
//
// Every route requires an authenticated browser session. The handler never
// touches plaintext: request bodies carry base64 AES-256-GCM ciphertext and
// nonces, which are stored and returned verbatim. Optimistic-concurrency
// conflicts are reported as 409 with the server's current row.
package handler

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/headercat/airbrew/internal/audit"
	"github.com/headercat/airbrew/internal/auth/session"
	"github.com/headercat/airbrew/internal/blob"
	"github.com/headercat/airbrew/internal/httpserver/requestip"
	"github.com/headercat/airbrew/internal/httpserver/response"
	"github.com/headercat/airbrew/internal/passwords/vault"
)

// Handler exposes the vault JSON endpoints.
type Handler struct {
	svc   *vault.Service
	audit *audit.Service
	blobs blob.Store
}

// New builds a Handler. blobs is required for attachment upload/download.
func New(svc *vault.Service, auditSvc *audit.Service, blobs blob.Store) *Handler {
	return &Handler{svc: svc, audit: auditSvc, blobs: blobs}
}

// RegisterRoutes mounts the vault endpoints on mux. All routes are under
// /api/vault and require a loaded session (see Module wiring in server.go).
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/vault/setup", h.setup)
	mux.HandleFunc("GET /api/vault/keys", h.getKeys)
	mux.HandleFunc("POST /api/vault/keys/rotate", h.rotateKeys)

	mux.HandleFunc("GET /api/vault/sync", h.sync)
	mux.HandleFunc("GET /api/vault/export", h.exportVault)
	mux.HandleFunc("POST /api/vault/import", h.importVault)

	mux.HandleFunc("POST /api/vault/folders", h.createFolder)
	mux.HandleFunc("PUT /api/vault/folders/{id}", h.updateFolder)
	mux.HandleFunc("DELETE /api/vault/folders/{id}", h.deleteFolder)

	mux.HandleFunc("POST /api/vault/items", h.createItem)
	mux.HandleFunc("GET /api/vault/items/{id}", h.getItem)
	mux.HandleFunc("PUT /api/vault/items/{id}", h.updateItem)
	mux.HandleFunc("DELETE /api/vault/items/{id}", h.deleteItem)
	mux.HandleFunc("GET /api/vault/items/{id}/revisions", h.listRevisions)
	mux.HandleFunc("POST /api/vault/items/{id}/revisions/{rid}/restore", h.restoreRevision)

	mux.HandleFunc("GET /api/vault/items/{id}/attachments", h.listAttachments)
	mux.HandleFunc("POST /api/vault/items/{id}/attachments", h.uploadAttachment)
	mux.HandleFunc("GET /api/vault/items/{id}/attachments/{aid}", h.downloadAttachment)
	mux.HandleFunc("DELETE /api/vault/items/{id}/attachments/{aid}", h.deleteAttachment)
}

// --- envelope ---------------------------------------------------------------

type envelopeReq struct {
	KDFAlgorithm        string `json:"kdf_algorithm"`
	KDFSalt             string `json:"kdf_salt"`
	KDFMemoryKiB        int    `json:"kdf_memory_kib"`
	KDFIterations       int    `json:"kdf_iterations"`
	KDFParallelism      int    `json:"kdf_parallelism"`
	ProtectedVaultKey   string `json:"protected_vault_key"`
	ProtectedVaultNonce string `json:"protected_vault_nonce"`
	CryptoVersion       int    `json:"crypto_version"`
	IfVersion           int64  `json:"if_version"`
}

type envelopeResp struct {
	KDFAlgorithm        string `json:"kdf_algorithm"`
	KDFSalt             string `json:"kdf_salt"`
	KDFMemoryKiB        int    `json:"kdf_memory_kib"`
	KDFIterations       int    `json:"kdf_iterations"`
	KDFParallelism      int    `json:"kdf_parallelism"`
	ProtectedVaultKey   string `json:"protected_vault_key"`
	ProtectedVaultNonce string `json:"protected_vault_nonce"`
	CryptoVersion       int    `json:"crypto_version"`
	Version             int64  `json:"version"`
	UpdatedAt           string `json:"updated_at"`
}

func toEnvelopeResp(env vault.KeyEnvelope) envelopeResp {
	return envelopeResp{
		KDFAlgorithm: env.KDFAlgorithm, KDFSalt: env.KDFSalt,
		KDFMemoryKiB: env.KDFMemoryKiB, KDFIterations: env.KDFIterations,
		KDFParallelism:    env.KDFParallelism,
		ProtectedVaultKey: env.ProtectedVaultKey, ProtectedVaultNonce: env.ProtectedVaultNonce,
		CryptoVersion: env.CryptoVersion,
		Version:       env.Version,
		UpdatedAt:     env.UpdatedAt.UTC().Format(time.RFC3339),
	}
}

func (h *Handler) setup(w http.ResponseWriter, r *http.Request) {
	sess, ok := requireSession(w, r)
	if !ok {
		return
	}
	var req envelopeReq
	if err := decodeJSON(r, &req); err != nil {
		response.Error(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	env := vault.KeyEnvelope{
		UserID: sess.UserID, KDFAlgorithm: req.KDFAlgorithm, KDFSalt: req.KDFSalt,
		KDFMemoryKiB: req.KDFMemoryKiB, KDFIterations: req.KDFIterations, KDFParallelism: req.KDFParallelism,
		ProtectedVaultKey: req.ProtectedVaultKey, ProtectedVaultNonce: req.ProtectedVaultNonce,
		CryptoVersion: req.CryptoVersion,
	}
	if err := h.svc.Setup(r.Context(), env); err != nil {
		if errors.Is(err, vault.ErrEnvelopeExists) {
			response.Error(w, http.StatusConflict, "envelope_exists", "vault is already set up")
			return
		}
		if errors.Is(err, vault.ErrInvalidInput) {
			response.Error(w, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
		response.Error(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	h.audit.Log(r.Context(), audit.Entry{
		EventType: "vault.setup", ActorUserID: sess.UserID,
		TargetType: "vault", TargetID: sess.UserID,
		IPAddress: clientIP(r), UserAgent: r.UserAgent(),
	})
	response.JSON(w, http.StatusCreated, map[string]bool{"ok": true})
}

func (h *Handler) getKeys(w http.ResponseWriter, r *http.Request) {
	sess, ok := requireSession(w, r)
	if !ok {
		return
	}
	env, err := h.svc.GetEnvelope(r.Context(), sess.UserID)
	if err != nil {
		if errors.Is(err, vault.ErrNotFound) {
			response.Error(w, http.StatusNotFound, "not_set_up", "vault has not been set up")
			return
		}
		response.Error(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	response.JSON(w, http.StatusOK, toEnvelopeResp(env))
}

func (h *Handler) rotateKeys(w http.ResponseWriter, r *http.Request) {
	sess, ok := requireSession(w, r)
	if !ok {
		return
	}
	var req envelopeReq
	if err := decodeJSON(r, &req); err != nil {
		response.Error(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	env := vault.KeyEnvelope{
		UserID: sess.UserID, KDFAlgorithm: req.KDFAlgorithm, KDFSalt: req.KDFSalt,
		KDFMemoryKiB: req.KDFMemoryKiB, KDFIterations: req.KDFIterations, KDFParallelism: req.KDFParallelism,
		ProtectedVaultKey: req.ProtectedVaultKey, ProtectedVaultNonce: req.ProtectedVaultNonce,
		CryptoVersion: req.CryptoVersion,
	}
	if err := h.svc.RotateEnvelope(r.Context(), env, req.IfVersion); err != nil {
		if errors.Is(err, vault.ErrNotFound) {
			response.Error(w, http.StatusNotFound, "not_set_up", "vault has not been set up")
			return
		}
		if errors.Is(err, vault.ErrInvalidInput) {
			response.Error(w, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
		writeVaultError(w, err)
		return
	}
	h.audit.Log(r.Context(), audit.Entry{
		EventType: "vault.keys_rotated", ActorUserID: sess.UserID,
		TargetType: "vault", TargetID: sess.UserID,
		IPAddress: clientIP(r), UserAgent: r.UserAgent(),
	})
	response.JSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// --- folders ---------------------------------------------------------------

type folderReq struct {
	ID            string `json:"id"`
	NameCipher    string `json:"name_cipher"`
	NameNonce     string `json:"name_nonce"`
	CryptoVersion int    `json:"crypto_version"`
	IfRevision    int64  `json:"if_revision"`
}

type folderResp struct {
	ID            string  `json:"id"`
	NameCipher    string  `json:"name_cipher"`
	NameNonce     string  `json:"name_nonce"`
	CryptoVersion int     `json:"crypto_version"`
	Revision      int64   `json:"revision"`
	CreatedAt     string  `json:"created_at"`
	UpdatedAt     string  `json:"updated_at"`
	DeletedAt     *string `json:"deleted_at"`
}

func toFolderResp(f vault.Folder) folderResp {
	out := folderResp{
		ID: f.ID, NameCipher: f.NameCipher, NameNonce: f.NameNonce,
		CryptoVersion: f.CryptoVersion,
		Revision:      f.Revision,
		CreatedAt:     f.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt:     f.UpdatedAt.UTC().Format(time.RFC3339),
	}
	if f.DeletedAt != nil {
		s := f.DeletedAt.UTC().Format(time.RFC3339)
		out.DeletedAt = &s
	}
	return out
}

func (h *Handler) createFolder(w http.ResponseWriter, r *http.Request) {
	sess, ok := requireSession(w, r)
	if !ok {
		return
	}
	var req folderReq
	if err := decodeJSON(r, &req); err != nil {
		response.Error(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	f, err := h.svc.CreateFolder(r.Context(), sess.UserID, req.ID, req.NameCipher, req.NameNonce)
	if err != nil {
		writeVaultError(w, err)
		return
	}
	h.auditVault(r, "vault.folder_created", f.ID, nil)
	response.JSON(w, http.StatusCreated, toFolderResp(f))
}

func (h *Handler) updateFolder(w http.ResponseWriter, r *http.Request) {
	sess, ok := requireSession(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	var req folderReq
	if err := decodeJSON(r, &req); err != nil {
		response.Error(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	f, err := h.svc.UpdateFolder(r.Context(), sess.UserID, id, req.NameCipher, req.NameNonce, req.IfRevision)
	if err != nil {
		writeVaultError(w, err)
		return
	}
	h.auditVault(r, "vault.folder_updated", f.ID, nil)
	response.JSON(w, http.StatusOK, toFolderResp(f))
}

func (h *Handler) deleteFolder(w http.ResponseWriter, r *http.Request) {
	sess, ok := requireSession(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	ifRev, _ := strconv.ParseInt(r.URL.Query().Get("if_revision"), 10, 64)
	if err := h.svc.DeleteFolder(r.Context(), sess.UserID, id, ifRev); err != nil {
		writeVaultError(w, err)
		return
	}
	h.auditVault(r, "vault.folder_deleted", id, nil)
	response.JSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// --- items -----------------------------------------------------------------

type itemReq struct {
	ID            string         `json:"id"`
	Type          vault.ItemType `json:"type"`
	FolderID      string         `json:"folder_id"`
	NameCipher    string         `json:"name_cipher"`
	NameNonce     string         `json:"name_nonce"`
	DataCipher    string         `json:"data_cipher"`
	DataNonce     string         `json:"data_nonce"`
	NotesCipher   string         `json:"notes_cipher"`
	NotesNonce    string         `json:"notes_nonce"`
	CryptoVersion int            `json:"crypto_version"`
	Favorite      bool           `json:"favorite"`
	Reprompt      bool           `json:"reprompt"`
	IfRevision    int64          `json:"if_revision"`
}

type itemResp struct {
	ID            string  `json:"id"`
	Type          string  `json:"type"`
	FolderID      string  `json:"folder_id"`
	NameCipher    string  `json:"name_cipher"`
	NameNonce     string  `json:"name_nonce"`
	DataCipher    string  `json:"data_cipher"`
	DataNonce     string  `json:"data_nonce"`
	NotesCipher   string  `json:"notes_cipher"`
	NotesNonce    string  `json:"notes_nonce"`
	CryptoVersion int     `json:"crypto_version"`
	Favorite      bool    `json:"favorite"`
	Reprompt      bool    `json:"reprompt"`
	Revision      int64   `json:"revision"`
	CreatedAt     string  `json:"created_at"`
	UpdatedAt     string  `json:"updated_at"`
	DeletedAt     *string `json:"deleted_at"`
}

func toItemResp(it vault.Item) itemResp {
	out := itemResp{
		ID: it.ID, Type: string(it.Type), FolderID: it.FolderID,
		NameCipher: it.NameCipher, NameNonce: it.NameNonce,
		DataCipher: it.DataCipher, DataNonce: it.DataNonce,
		NotesCipher: it.NotesCipher, NotesNonce: it.NotesNonce,
		CryptoVersion: it.CryptoVersion,
		Favorite:      it.Favorite, Reprompt: it.Reprompt, Revision: it.Revision,
		CreatedAt: it.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt: it.UpdatedAt.UTC().Format(time.RFC3339),
	}
	if it.DeletedAt != nil {
		s := it.DeletedAt.UTC().Format(time.RFC3339)
		out.DeletedAt = &s
	}
	return out
}

func itemInput(req itemReq) vault.ItemInput {
	return vault.ItemInput{
		ID:   req.ID,
		Type: req.Type, FolderID: req.FolderID,
		NameCipher: req.NameCipher, NameNonce: req.NameNonce,
		DataCipher: req.DataCipher, DataNonce: req.DataNonce,
		NotesCipher: req.NotesCipher, NotesNonce: req.NotesNonce,
		CryptoVersion: req.CryptoVersion,
		Favorite:      req.Favorite, Reprompt: req.Reprompt,
	}
}

func (h *Handler) createItem(w http.ResponseWriter, r *http.Request) {
	sess, ok := requireSession(w, r)
	if !ok {
		return
	}
	var req itemReq
	if err := decodeJSON(r, &req); err != nil {
		response.Error(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	it, err := h.svc.CreateItem(r.Context(), sess.UserID, itemInput(req))
	if err != nil {
		writeVaultError(w, err)
		return
	}
	h.auditVault(r, "vault.item_created", it.ID, map[string]any{"type": string(it.Type)})
	response.JSON(w, http.StatusCreated, toItemResp(it))
}

func (h *Handler) getItem(w http.ResponseWriter, r *http.Request) {
	sess, ok := requireSession(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	it, err := h.svc.GetItem(r.Context(), sess.UserID, id)
	if err != nil {
		writeVaultError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, toItemResp(it))
}

func (h *Handler) updateItem(w http.ResponseWriter, r *http.Request) {
	sess, ok := requireSession(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	var req itemReq
	if err := decodeJSON(r, &req); err != nil {
		response.Error(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	it, err := h.svc.UpdateItem(r.Context(), sess.UserID, id, itemInput(req), req.IfRevision)
	if err != nil {
		writeVaultError(w, err)
		return
	}
	h.auditVault(r, "vault.item_updated", it.ID, nil)
	response.JSON(w, http.StatusOK, toItemResp(it))
}

func (h *Handler) deleteItem(w http.ResponseWriter, r *http.Request) {
	sess, ok := requireSession(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	ifRev, _ := strconv.ParseInt(r.URL.Query().Get("if_revision"), 10, 64)
	blobPaths, err := h.svc.DeleteItem(r.Context(), sess.UserID, id, ifRev)
	if err != nil {
		writeVaultError(w, err)
		return
	}
	// Best-effort: purge the underlying blobs now that the rows are gone.
	if h.blobs != nil {
		for _, p := range blobPaths {
			_ = h.blobs.Delete(r.Context(), p)
		}
	}
	h.auditVault(r, "vault.item_deleted", id, nil)
	response.JSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// --- sync ------------------------------------------------------------------

type syncResp struct {
	Cursor  int64        `json:"cursor"`
	Folders []folderResp `json:"folders"`
	Items   []itemResp   `json:"items"`
	HasMore bool         `json:"has_more"`
}

const defaultSyncLimit = 500
const maxSyncLimit = 2000

func (h *Handler) sync(w http.ResponseWriter, r *http.Request) {
	sess, ok := requireSession(w, r)
	if !ok {
		return
	}
	since, _ := strconv.ParseInt(r.URL.Query().Get("since"), 10, 64)
	limit, _ := strconv.ParseInt(r.URL.Query().Get("limit"), 10, 64)
	if limit <= 0 {
		limit = defaultSyncLimit
	}
	if limit > maxSyncLimit {
		limit = maxSyncLimit
	}
	res, err := h.svc.Sync(r.Context(), sess.UserID, since, limit)
	if err != nil {
		response.Error(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	out := syncResp{Cursor: res.Cursor, HasMore: res.HasMore, Folders: []folderResp{}, Items: []itemResp{}}
	for _, f := range res.Folders {
		out.Folders = append(out.Folders, toFolderResp(f))
	}
	for _, it := range res.Items {
		out.Items = append(out.Items, toItemResp(it))
	}
	response.JSON(w, http.StatusOK, out)
}

// --- history ----------------------------------------------------------------

type itemRevisionResp struct {
	ID            string `json:"id"`
	Type          string `json:"type"`
	FolderID      string `json:"folder_id"`
	NameCipher    string `json:"name_cipher"`
	NameNonce     string `json:"name_nonce"`
	DataCipher    string `json:"data_cipher"`
	DataNonce     string `json:"data_nonce"`
	NotesCipher   string `json:"notes_cipher"`
	NotesNonce    string `json:"notes_nonce"`
	CryptoVersion int    `json:"crypto_version"`
	Favorite      bool   `json:"favorite"`
	Reprompt      bool   `json:"reprompt"`
	Revision      int64  `json:"revision"`
	CreatedAt     string `json:"created_at"`
}

func (h *Handler) listRevisions(w http.ResponseWriter, r *http.Request) {
	sess, ok := requireSession(w, r)
	if !ok {
		return
	}
	itemID := r.PathValue("id")
	revs, err := h.svc.ListItemRevisions(r.Context(), sess.UserID, itemID)
	if err != nil {
		response.Error(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	out := make([]itemRevisionResp, 0, len(revs))
	for _, rv := range revs {
		out = append(out, itemRevisionResp{
			ID: rv.ID, Type: string(rv.Type), FolderID: rv.FolderID,
			NameCipher: rv.NameCipher, NameNonce: rv.NameNonce,
			DataCipher: rv.DataCipher, DataNonce: rv.DataNonce,
			NotesCipher: rv.NotesCipher, NotesNonce: rv.NotesNonce,
			CryptoVersion: rv.CryptoVersion,
			Favorite:      rv.Favorite, Reprompt: rv.Reprompt,
			Revision: rv.Revision, CreatedAt: rv.CreatedAt.UTC().Format(time.RFC3339),
		})
	}
	response.JSON(w, http.StatusOK, map[string]any{"revisions": out})
}

func (h *Handler) restoreRevision(w http.ResponseWriter, r *http.Request) {
	sess, ok := requireSession(w, r)
	if !ok {
		return
	}
	itemID := r.PathValue("id")
	revID := r.PathValue("rid")
	ifRev, _ := strconv.ParseInt(r.URL.Query().Get("if_revision"), 10, 64)
	it, err := h.svc.RestoreItemRevision(r.Context(), sess.UserID, itemID, revID, ifRev)
	if err != nil {
		writeVaultError(w, err)
		return
	}
	h.auditVault(r, "vault.item_restored", it.ID, map[string]any{"revision_id": revID})
	response.JSON(w, http.StatusOK, toItemResp(it))
}

// --- export / import --------------------------------------------------------

type exportResp struct {
	Envelope    envelopeResp           `json:"envelope"`
	Folders     []folderResp           `json:"folders"`
	Items       []itemResp             `json:"items"`
	Attachments []exportAttachmentResp `json:"attachments"`
}

type exportAttachmentResp struct {
	ID            string `json:"id"`
	ItemID        string `json:"item_id"`
	NameCipher    string `json:"name_cipher"`
	NameNonce     string `json:"name_nonce"`
	FileKeyCipher string `json:"file_key_cipher"`
	FileKeyNonce  string `json:"file_key_nonce"`
	CryptoVersion int    `json:"crypto_version"`
	SizeBytes     int64  `json:"size_bytes"`
	Payload       string `json:"payload"`
}

func (h *Handler) exportVault(w http.ResponseWriter, r *http.Request) {
	sess, ok := requireSession(w, r)
	if !ok {
		return
	}
	env, folders, items, attachments, err := h.svc.ExportBundle(r.Context(), sess.UserID)
	if err != nil {
		writeVaultError(w, err)
		return
	}
	out := exportResp{Envelope: toEnvelopeResp(env), Folders: []folderResp{}, Items: []itemResp{}, Attachments: []exportAttachmentResp{}}
	for _, f := range folders {
		out.Folders = append(out.Folders, toFolderResp(f))
	}
	for _, it := range items {
		out.Items = append(out.Items, toItemResp(it))
	}
	if len(attachments) > 0 && h.blobs == nil {
		response.Error(w, http.StatusServiceUnavailable, "unavailable", "blob store not configured")
		return
	}
	for _, a := range attachments {
		body, _, err := h.blobs.Open(r.Context(), a.BlobPath)
		if err != nil {
			writeVaultError(w, fmt.Errorf("%w: attachment blob missing", vault.ErrNotFound))
			return
		}
		payload, err := io.ReadAll(io.LimitReader(body, maxAttachmentBytes+1))
		_ = body.Close()
		if err != nil {
			response.Error(w, http.StatusInternalServerError, "internal_error", err.Error())
			return
		}
		if len(payload) > maxAttachmentBytes {
			response.Error(w, http.StatusInternalServerError, "internal_error", "attachment exceeds export cap")
			return
		}
		out.Attachments = append(out.Attachments, exportAttachmentResp{
			ID: a.ID, ItemID: a.ItemID,
			NameCipher: a.NameCipher, NameNonce: a.NameNonce,
			FileKeyCipher: a.FileKeyCipher, FileKeyNonce: a.FileKeyNonce,
			CryptoVersion: a.CryptoVersion,
			SizeBytes:     a.SizeBytes,
			Payload:       base64.StdEncoding.EncodeToString(payload),
		})
	}
	body, err := json.Marshal(out)
	if err != nil {
		response.Error(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	if int64(len(body)) > maxExportJSONBody {
		response.Error(w, http.StatusRequestEntityTooLarge, "too_large", "vault backup exceeds export cap")
		return
	}
	h.auditVault(r, "vault.export", sess.UserID, nil)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

type importReq struct {
	Folders     []importFolderReq     `json:"folders"`
	Items       []importItemReq       `json:"items"`
	Attachments []importAttachmentReq `json:"attachments"`
}

type importFolderReq struct {
	ID            string `json:"id"`
	NameCipher    string `json:"name_cipher"`
	NameNonce     string `json:"name_nonce"`
	CryptoVersion int    `json:"crypto_version"`
	DeletedAt     string `json:"deleted_at"`
}

type importItemReq struct {
	ID            string         `json:"id"`
	Type          vault.ItemType `json:"type"`
	FolderID      string         `json:"folder_id"`
	NameCipher    string         `json:"name_cipher"`
	NameNonce     string         `json:"name_nonce"`
	DataCipher    string         `json:"data_cipher"`
	DataNonce     string         `json:"data_nonce"`
	NotesCipher   string         `json:"notes_cipher"`
	NotesNonce    string         `json:"notes_nonce"`
	CryptoVersion int            `json:"crypto_version"`
	Favorite      bool           `json:"favorite"`
	Reprompt      bool           `json:"reprompt"`
	DeletedAt     string         `json:"deleted_at"`
}

type importAttachmentReq struct {
	ID            string `json:"id"`
	ItemID        string `json:"item_id"`
	NameCipher    string `json:"name_cipher"`
	NameNonce     string `json:"name_nonce"`
	FileKeyCipher string `json:"file_key_cipher"`
	FileKeyNonce  string `json:"file_key_nonce"`
	CryptoVersion int    `json:"crypto_version"`
	SizeBytes     int64  `json:"size_bytes"`
	Payload       string `json:"payload"`
}

func (h *Handler) importVault(w http.ResponseWriter, r *http.Request) {
	sess, ok := requireSession(w, r)
	if !ok {
		return
	}
	var req importReq
	if err := decodeJSONLimit(r, &req, maxImportJSONBody); err != nil {
		response.Error(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	folders := make([]vault.Folder, 0, len(req.Folders))
	for _, f := range req.Folders {
		deletedAt, err := parseOptionalTime(f.DeletedAt)
		if err != nil {
			response.Error(w, http.StatusBadRequest, "invalid_request", "invalid folder deleted_at")
			return
		}
		folders = append(folders, vault.Folder{
			ID: f.ID, NameCipher: f.NameCipher, NameNonce: f.NameNonce,
			CryptoVersion: f.CryptoVersion, DeletedAt: deletedAt,
		})
	}
	items := make([]vault.Item, 0, len(req.Items))
	for _, it := range req.Items {
		if !it.Type.Valid() {
			response.Error(w, http.StatusBadRequest, "invalid_request", "invalid item type in import bundle")
			return
		}
		deletedAt, err := parseOptionalTime(it.DeletedAt)
		if err != nil {
			response.Error(w, http.StatusBadRequest, "invalid_request", "invalid item deleted_at")
			return
		}
		items = append(items, vault.Item{
			ID: it.ID, Type: it.Type, FolderID: it.FolderID,
			NameCipher: it.NameCipher, NameNonce: it.NameNonce,
			DataCipher: it.DataCipher, DataNonce: it.DataNonce,
			NotesCipher: it.NotesCipher, NotesNonce: it.NotesNonce,
			CryptoVersion: it.CryptoVersion,
			Favorite:      it.Favorite, Reprompt: it.Reprompt, DeletedAt: deletedAt,
		})
	}
	var attachments []vault.Attachment
	var savedBlobPaths []string
	if len(req.Attachments) > 0 && h.blobs == nil {
		response.Error(w, http.StatusServiceUnavailable, "unavailable", "blob store not configured")
		return
	}
	for _, a := range req.Attachments {
		payload, err := base64.StdEncoding.Strict().DecodeString(a.Payload)
		if err != nil {
			cleanupBlobs(r.Context(), h.blobs, savedBlobPaths)
			response.Error(w, http.StatusBadRequest, "invalid_request", "attachment payload must be base64")
			return
		}
		if len(payload) > maxAttachmentBytes {
			cleanupBlobs(r.Context(), h.blobs, savedBlobPaths)
			response.Error(w, http.StatusBadRequest, "invalid_request", "attachment payload too large")
			return
		}
		blobPath, err := h.blobs.Save(r.Context(), "vault-attachments", "application/octet-stream", bytes.NewReader(payload))
		if err != nil {
			cleanupBlobs(r.Context(), h.blobs, savedBlobPaths)
			response.Error(w, http.StatusInternalServerError, "internal_error", err.Error())
			return
		}
		savedBlobPaths = append(savedBlobPaths, blobPath)
		attachments = append(attachments, vault.Attachment{
			ID: a.ID, ItemID: a.ItemID, BlobPath: blobPath, SizeBytes: a.SizeBytes,
			FileKeyCipher: a.FileKeyCipher, FileKeyNonce: a.FileKeyNonce,
			NameCipher: a.NameCipher, NameNonce: a.NameNonce,
			CryptoVersion: a.CryptoVersion,
		})
	}
	fc, ic, ac, err := h.svc.ImportBundle(r.Context(), sess.UserID, folders, items, attachments)
	if err != nil {
		cleanupBlobs(r.Context(), h.blobs, savedBlobPaths)
		writeVaultError(w, err)
		return
	}
	h.auditVault(r, "vault.import", sess.UserID, map[string]any{"count": fc + ic + ac})
	response.JSON(w, http.StatusCreated, map[string]int64{"folders": fc, "items": ic, "attachments": ac})
}

// --- helpers ---------------------------------------------------------------

// requireSession loads the session and writes a 401 + false if absent.
func requireSession(w http.ResponseWriter, r *http.Request) (*session.Session, bool) {
	sess, ok := session.FromContext(r.Context())
	if !ok {
		response.Error(w, http.StatusUnauthorized, "unauthorized", "no active session")
		return nil, false
	}
	return sess, true
}

// auditVault records a vault event scoped to the session user. It is best-effort
// (the audit service never returns an error that would fail the request).
func (h *Handler) auditVault(r *http.Request, eventType, targetID string, meta map[string]any) {
	if h.audit == nil {
		return
	}
	sess, ok := session.FromContext(r.Context())
	if !ok {
		return
	}
	h.audit.Log(r.Context(), audit.Entry{
		EventType:   eventType,
		ActorUserID: sess.UserID,
		TargetType:  "vault",
		TargetID:    targetID,
		IPAddress:   clientIP(r),
		UserAgent:   r.UserAgent(),
		Metadata:    meta,
	})
}

// writeVaultError maps a vault service error to an HTTP status. Conflict errors
// carry the server's current row so the client can merge.
func writeVaultError(w http.ResponseWriter, err error) {
	if ce := vault.AsConflict(err); ce != nil {
		body := map[string]any{"error": "conflict", "error_description": "revision mismatch"}
		switch cur := ce.CurrentRow.(type) {
		case *vault.Item:
			body["current"] = toItemResp(*cur)
		case *vault.Folder:
			body["current"] = toFolderResp(*cur)
		case *vault.KeyEnvelope:
			body["current"] = toEnvelopeResp(*cur)
		}
		response.JSON(w, http.StatusConflict, body)
		return
	}
	switch {
	case errors.Is(err, vault.ErrNotFound):
		response.Error(w, http.StatusNotFound, "not_found", "vault entry not found")
	case errors.Is(err, vault.ErrInvalidInput):
		response.Error(w, http.StatusBadRequest, "invalid_request", err.Error())
	default:
		response.Error(w, http.StatusInternalServerError, "internal_error", err.Error())
	}
}

// maxJSONBody caps the size of any vault JSON request body. Vault payloads are
// small (ciphertext + nonces), so 256 KiB is generous while preventing a
// malicious oversized body from exhausting server memory. Attachment uploads
// are handled separately (multipart) with their own cap.
const maxJSONBody = 256 << 10
const maxImportJSONBody = 256 << 20

// Export must only produce backup JSON that the matching import endpoint can
// accept, with a little headroom for transfer/client-side wrappers.
const maxExportJSONBody = maxImportJSONBody - (4 << 20)

func decodeJSON(r *http.Request, v any) error {
	return decodeJSONLimit(r, v, maxJSONBody)
}

func decodeJSONLimit(r *http.Request, v any, limit int64) error {
	ct := r.Header.Get("Content-Type")
	if !strings.Contains(ct, "application/json") {
		return errors.New("content-type must be application/json")
	}
	r.Body = http.MaxBytesReader(nil, r.Body, limit)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}

func parseOptionalTime(value string) (*time.Time, error) {
	if value == "" {
		return nil, nil
	}
	t, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return nil, err
	}
	u := t.UTC()
	return &u, nil
}

func cleanupBlobs(ctx context.Context, store blob.Store, paths []string) {
	if store == nil {
		return
	}
	for _, p := range paths {
		_ = store.Delete(ctx, p)
	}
}

func clientIP(r *http.Request) string {
	return requestip.DirectClientIP(r)
}

// --- attachments -----------------------------------------------------------

const maxAttachmentBytes = 10 << 20 // 10 MiB ciphertext upload cap

type attachmentResp struct {
	ID            string `json:"id"`
	NameCipher    string `json:"name_cipher"`
	NameNonce     string `json:"name_nonce"`
	FileKeyCipher string `json:"file_key_cipher"`
	FileKeyNonce  string `json:"file_key_nonce"`
	CryptoVersion int    `json:"crypto_version"`
	SizeBytes     int64  `json:"size_bytes"`
	CreatedAt     string `json:"created_at"`
}

func toAttachmentResp(a vault.Attachment) attachmentResp {
	return attachmentResp{
		ID: a.ID, NameCipher: a.NameCipher, NameNonce: a.NameNonce,
		FileKeyCipher: a.FileKeyCipher, FileKeyNonce: a.FileKeyNonce,
		CryptoVersion: a.CryptoVersion,
		SizeBytes:     a.SizeBytes, CreatedAt: a.CreatedAt.UTC().Format(time.RFC3339),
	}
}

func (h *Handler) listAttachments(w http.ResponseWriter, r *http.Request) {
	sess, ok := requireSession(w, r)
	if !ok {
		return
	}
	itemID := r.PathValue("id")
	atts, err := h.svc.ListAttachments(r.Context(), sess.UserID, itemID)
	if err != nil {
		response.Error(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	out := make([]attachmentResp, 0, len(atts))
	for _, a := range atts {
		out = append(out, toAttachmentResp(a))
	}
	response.JSON(w, http.StatusOK, map[string]any{"attachments": out})
}

func (h *Handler) uploadAttachment(w http.ResponseWriter, r *http.Request) {
	sess, ok := requireSession(w, r)
	if !ok {
		return
	}
	if h.blobs == nil {
		response.Error(w, http.StatusServiceUnavailable, "unavailable", "blob store not configured")
		return
	}
	itemID := r.PathValue("id")
	// Cap total upload size.
	r.Body = http.MaxBytesReader(w, r.Body, maxAttachmentBytes+2<<10)
	if err := r.ParseMultipartForm(32 << 10); err != nil {
		response.Error(w, http.StatusBadRequest, "invalid_request", "failed to parse multipart: "+err.Error())
		return
	}
	file, _, err := r.FormFile("file")
	if err != nil {
		response.Error(w, http.StatusBadRequest, "invalid_request", "file field is required")
		return
	}
	defer file.Close()

	fkCipher := r.FormValue("file_key_cipher")
	fkNonce := r.FormValue("file_key_nonce")
	nameCipher := r.FormValue("name_cipher")
	nameNonce := r.FormValue("name_nonce")
	attachmentID := r.FormValue("id")
	sizeBytes, _ := strconv.ParseInt(r.FormValue("size_bytes"), 10, 64)
	sealed, err := io.ReadAll(io.LimitReader(file, maxAttachmentBytes+1))
	if err != nil {
		response.Error(w, http.StatusBadRequest, "invalid_request", "failed to read file: "+err.Error())
		return
	}
	if len(sealed) > maxAttachmentBytes {
		response.Error(w, http.StatusBadRequest, "invalid_request", "attachment too large")
		return
	}
	if sizeBytes < 0 || int64(len(sealed)) != sizeBytes+28 {
		response.Error(w, http.StatusBadRequest, "invalid_request", "attachment size mismatch")
		return
	}

	// Stream the encrypted payload straight to the blob store.
	blobPath, err := h.blobs.Save(r.Context(), "vault-attachments", "application/octet-stream", bytes.NewReader(sealed))
	if err != nil {
		response.Error(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	att, err := h.svc.CreateAttachment(r.Context(), sess.UserID, itemID, blobPath, sizeBytes,
		fkCipher, fkNonce, nameCipher, nameNonce, attachmentID)
	if err != nil {
		// Best-effort: clean up the orphaned blob on DB failure.
		_ = h.blobs.Delete(r.Context(), blobPath)
		writeVaultError(w, err)
		return
	}
	response.JSON(w, http.StatusCreated, toAttachmentResp(att))
}

func (h *Handler) downloadAttachment(w http.ResponseWriter, r *http.Request) {
	sess, ok := requireSession(w, r)
	if !ok {
		return
	}
	itemID := r.PathValue("id")
	att, err := h.svc.GetAttachment(r.Context(), sess.UserID, itemID, r.PathValue("aid"))
	if err != nil {
		writeVaultError(w, err)
		return
	}
	body, _, err := h.blobs.Open(r.Context(), att.BlobPath)
	if err != nil {
		response.Error(w, http.StatusNotFound, "not_found", "attachment blob missing")
		return
	}
	defer body.Close()
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", "attachment")
	if _, err := io.Copy(w, body); err != nil {
		return
	}
}

func (h *Handler) deleteAttachment(w http.ResponseWriter, r *http.Request) {
	sess, ok := requireSession(w, r)
	if !ok {
		return
	}
	itemID := r.PathValue("id")
	aid := r.PathValue("aid")
	att, err := h.svc.GetAttachment(r.Context(), sess.UserID, itemID, aid)
	if err != nil {
		writeVaultError(w, err)
		return
	}
	if err := h.svc.DeleteAttachment(r.Context(), sess.UserID, itemID, aid); err != nil {
		writeVaultError(w, err)
		return
	}
	if h.blobs != nil {
		_ = h.blobs.Delete(r.Context(), att.BlobPath)
	}
	response.JSON(w, http.StatusOK, map[string]bool{"ok": true})
}
