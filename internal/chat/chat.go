// Package chat implements user-to-user real-time messaging.
package chat

import (
	"database/sql"
	"net/http"

	"github.com/headercat/airbrew/internal/audit"
	"github.com/headercat/airbrew/internal/modules"
)

type Module struct {
	state *modules.State
	h     *Handler
}

func New(db *sql.DB, state *modules.State, auditSvc *audit.Service) *Module {
	repo := NewRepository(db)
	hub := NewHub()
	svc := NewService(repo, hub)
	return &Module{state: state, h: NewHandler(state, svc, repo, auditSvc)}
}

func (m *Module) RegisterPublicRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/chat/status", m.h.status)
}

func (m *Module) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/chat/users", m.h.searchUsers)
	mux.HandleFunc("GET /api/chat/rooms", m.h.listRooms)
	mux.HandleFunc("POST /api/chat/rooms", m.h.createRoom)
	mux.HandleFunc("GET /api/chat/rooms/{id}", m.h.getRoom)
	mux.HandleFunc("PATCH /api/chat/rooms/{id}", m.h.patchRoom)
	mux.HandleFunc("DELETE /api/chat/rooms/{id}", m.h.leaveRoom)
	mux.HandleFunc("GET /api/chat/rooms/{id}/messages", m.h.listMessages)
	mux.HandleFunc("POST /api/chat/rooms/{id}/messages", m.h.sendMessage)
	mux.HandleFunc("PATCH /api/chat/rooms/{id}/messages/{msg}", m.h.editMessage)
	mux.HandleFunc("DELETE /api/chat/rooms/{id}/messages/{msg}", m.h.deleteMessage)
	mux.HandleFunc("POST /api/chat/rooms/{id}/read", m.h.markRead)
	mux.HandleFunc("GET /api/chat/events", m.h.events)
}
