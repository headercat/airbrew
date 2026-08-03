// Package httpserver boots the HTTP server with the standard middleware chain.
package httpserver

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/headercat/airbrew/internal/httpserver/middleware"
)

// Deps are the inputs required to build a Server.
type Deps struct {
	Addr   string
	Mux    *http.ServeMux
	Logger *slog.Logger
	// StrictSecurity, when true, emits a strict Content-Security-Policy (and is
	// appropriate for the production SPA build). In dev (Vite proxy) it should
	// be false so HMR inline scripts/WebSocket are not blocked. Baseline
	// defensive headers and the CSRF check are always applied.
	StrictSecurity bool
}

// Server is a configured *http.Server with graceful shutdown.
type Server struct {
	httpSrv *http.Server
	logger  *slog.Logger
}

// New builds a Server wrapping the given mux with the standard middleware chain.
func New(d Deps) *Server {
	handler := middleware.Chain(
		d.Mux,
		middleware.SecurityHeaders(d.StrictSecurity),
		middleware.CSRF,
		middleware.RequestID,
		middleware.Recover(d.Logger),
		middleware.AccessLog(d.Logger),
	)
	// WriteTimeout is intentionally 0: it is an end-to-end deadline that would
	// force-close long-lived streaming responses (SSE for chat/AI) after 30s.
	// Slowloris protection is provided by ReadHeaderTimeout; per-handler write
	// deadlines can be applied via http.NewResponseController where needed.
	return &Server{
		httpSrv: &http.Server{
			Addr:              d.Addr,
			Handler:           handler,
			ReadHeaderTimeout: 10 * time.Second,
			ReadTimeout:       60 * time.Second,
			WriteTimeout:      0,
			IdleTimeout:       120 * time.Second,
		},
		logger: d.Logger,
	}
}

// Start blocks until the server stops (graceful shutdown returns nil).
func (s *Server) Start() error {
	s.logger.Info("http server starting", "addr", s.httpSrv.Addr)
	if err := s.httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// Shutdown gracefully drains in-flight requests.
func (s *Server) Shutdown(ctx context.Context) error {
	return s.httpSrv.Shutdown(ctx)
}
