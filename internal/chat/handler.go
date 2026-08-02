package chat

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/headercat/airbrew/internal/audit"
	"github.com/headercat/airbrew/internal/auth/session"
	"github.com/headercat/airbrew/internal/httpserver/requestip"
	"github.com/headercat/airbrew/internal/httpserver/response"
	"github.com/headercat/airbrew/internal/modules"
)

type Handler struct {
	state *modules.State
	svc   *Service
	repo  *Repository
	audit *audit.Service
}

func NewHandler(state *modules.State, svc *Service, repo *Repository, auditSvc *audit.Service) *Handler {
	return &Handler{state: state, svc: svc, repo: repo, audit: auditSvc}
}

func (h *Handler) status(w http.ResponseWriter, r *http.Request) {
	enabled := true
	if h.state != nil {
		if v, err := h.state.IsEnabled(r.Context(), "chat"); err == nil {
			enabled = v
		}
	}
	status := "ok"
	if !enabled {
		status = "disabled"
	}
	response.JSON(w, http.StatusOK, map[string]string{
		"module": "chat", "status": status, "enabled": boolStr(enabled),
	})
}

func (h *Handler) searchUsers(w http.ResponseWriter, r *http.Request) {
	userID, ok := currentUserID(w, r)
	if !ok {
		return
	}
	users, err := h.svc.SearchUsers(r.Context(), userID, r.URL.Query().Get("q"))
	if err != nil {
		h.respondErr(w, err)
		return
	}
	response.JSON(w, http.StatusOK, map[string]any{"users": users})
}

func (h *Handler) listRooms(w http.ResponseWriter, r *http.Request) {
	userID, ok := currentUserID(w, r)
	if !ok {
		return
	}
	rooms, err := h.svc.ListRooms(r.Context(), userID)
	if err != nil {
		h.respondErr(w, err)
		return
	}
	response.JSON(w, http.StatusOK, map[string]any{"rooms": rooms})
}

type createRoomReq struct {
	Kind      string   `json:"kind"`
	Title     string   `json:"title"`
	MemberIDs []string `json:"member_ids"`
}

func (h *Handler) createRoom(w http.ResponseWriter, r *http.Request) {
	userID, ok := currentUserID(w, r)
	if !ok {
		return
	}
	var req createRoomReq
	if err := decodeJSON(r, &req); err != nil {
		response.Error(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	room, created, err := h.svc.CreateRoom(r.Context(), userID, req.Kind, req.Title, req.MemberIDs)
	if err != nil {
		h.respondErr(w, err)
		return
	}
	if created {
		h.logAudit(r, "chat.room_created", room.ID, map[string]any{"kind": room.Kind})
	}
	status := http.StatusCreated
	if !created {
		status = http.StatusOK
	}
	response.JSON(w, status, room)
}

func (h *Handler) getRoom(w http.ResponseWriter, r *http.Request) {
	userID, ok := currentUserID(w, r)
	if !ok {
		return
	}
	room, err := h.svc.GetRoom(r.Context(), userID, r.PathValue("id"))
	if err != nil {
		h.respondErr(w, err)
		return
	}
	response.JSON(w, http.StatusOK, room)
}

type patchRoomReq struct {
	Title string `json:"title"`
}

func (h *Handler) patchRoom(w http.ResponseWriter, r *http.Request) {
	userID, ok := currentUserID(w, r)
	if !ok {
		return
	}
	var req patchRoomReq
	if err := decodeJSON(r, &req); err != nil {
		response.Error(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	room, err := h.svc.RenameRoom(r.Context(), userID, r.PathValue("id"), req.Title)
	if err != nil {
		h.respondErr(w, err)
		return
	}
	h.logAudit(r, "chat.room_renamed", room.ID, nil)
	response.JSON(w, http.StatusOK, room)
}

func (h *Handler) leaveRoom(w http.ResponseWriter, r *http.Request) {
	userID, ok := currentUserID(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	if err := h.svc.LeaveRoom(r.Context(), userID, id); err != nil {
		h.respondErr(w, err)
		return
	}
	h.logAudit(r, "chat.room_left", id, nil)
	response.JSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (h *Handler) listMessages(w http.ResponseWriter, r *http.Request) {
	userID, ok := currentUserID(w, r)
	if !ok {
		return
	}
	beforeSeq, _ := strconv.ParseInt(r.URL.Query().Get("before_seq"), 10, 64)
	msgs, err := h.svc.ListMessages(r.Context(), userID, r.PathValue("id"), beforeSeq)
	if err != nil {
		h.respondErr(w, err)
		return
	}
	response.JSON(w, http.StatusOK, map[string]any{"messages": msgs})
}

type sendMessageReq struct {
	Body string `json:"body"`
}

func (h *Handler) sendMessage(w http.ResponseWriter, r *http.Request) {
	userID, ok := currentUserID(w, r)
	if !ok {
		return
	}
	var req sendMessageReq
	if err := decodeJSON(r, &req); err != nil {
		response.Error(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	msg, err := h.svc.SendMessage(r.Context(), userID, r.PathValue("id"), req.Body)
	if err != nil {
		h.respondErr(w, err)
		return
	}
	h.logAudit(r, "chat.message_sent", msg.RoomID, map[string]any{"seq": msg.Seq})
	response.JSON(w, http.StatusCreated, msg)
}

type markReadReq struct {
	Seq int64 `json:"seq"`
}

func (h *Handler) markRead(w http.ResponseWriter, r *http.Request) {
	userID, ok := currentUserID(w, r)
	if !ok {
		return
	}
	var req markReadReq
	if r.Body != http.NoBody {
		if err := decodeJSON(r, &req); err != nil {
			response.Error(w, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
	}
	seq, err := h.svc.MarkRead(r.Context(), userID, r.PathValue("id"), req.Seq)
	if err != nil {
		h.respondErr(w, err)
		return
	}
	response.JSON(w, http.StatusOK, map[string]any{"ok": true, "seq": seq})
}

func (h *Handler) events(w http.ResponseWriter, r *http.Request) {
	userID, ok := currentUserID(w, r)
	if !ok {
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		response.Error(w, http.StatusInternalServerError, "streaming_unsupported", "streaming unsupported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	ch, cancel := h.svc.Subscribe(userID)
	defer cancel()
	writeSSE(w, "ready", map[string]bool{"ok": true})
	flusher.Flush()

	tick := time.NewTicker(25 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-tick.C:
			_, _ = fmt.Fprint(w, ": ping\n\n")
			flusher.Flush()
		case ev := <-ch:
			writeSSE(w, ev.Type, ev)
			flusher.Flush()
		}
	}
}

func (h *Handler) respondErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrNotFound):
		response.Error(w, http.StatusNotFound, "not_found", "chat resource not found")
	case errors.Is(err, ErrForbidden):
		response.Error(w, http.StatusForbidden, "forbidden", "not allowed")
	case errors.Is(err, ErrInvalidInput):
		response.Error(w, http.StatusBadRequest, "invalid_request", err.Error())
	case errors.Is(err, ErrConflict):
		response.Error(w, http.StatusConflict, "conflict", "please retry")
	default:
		response.Error(w, http.StatusInternalServerError, "internal_error", "chat operation failed")
	}
}

func (h *Handler) logAudit(r *http.Request, event, targetID string, meta map[string]any) {
	sess, _ := session.FromContext(r.Context())
	actor := ""
	if sess != nil {
		actor = sess.UserID
	}
	h.audit.Log(r.Context(), audit.Entry{
		EventType:   event,
		ActorUserID: actor,
		TargetType:  "chat_room",
		TargetID:    targetID,
		IPAddress:   clientIP(r),
		UserAgent:   r.UserAgent(),
		Metadata:    meta,
	})
}

func currentUserID(w http.ResponseWriter, r *http.Request) (string, bool) {
	sess, ok := session.FromContext(r.Context())
	if !ok || sess.UserID == "" {
		response.Error(w, http.StatusUnauthorized, "unauthorized", "no active session")
		return "", false
	}
	return sess.UserID, true
}

func decodeJSON(r *http.Request, v any) error {
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 64<<10))
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}

func writeSSE(w http.ResponseWriter, event string, v any) {
	b, _ := json.Marshal(v)
	_, _ = fmt.Fprintf(w, "event: %s\n", event)
	_, _ = fmt.Fprintf(w, "data: %s\n\n", b)
}

func clientIP(r *http.Request) string {
	return requestip.DirectClientIP(r)
}

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}
