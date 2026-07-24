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
		middleware.RequestID,
		middleware.Recover(d.Logger),
		middleware.AccessLog(d.Logger),
	)
	return &Server{
		httpSrv: &http.Server{
			Addr:              d.Addr,
			Handler:           handler,
			ReadHeaderTimeout: 10 * time.Second,
			ReadTimeout:       30 * time.Second,
			WriteTimeout:      30 * time.Second,
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
