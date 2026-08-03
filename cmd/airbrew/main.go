// Command airbrew is the single executable that serves the entire product.
//
// It embeds the React SPA, serves the JSON API, and runs database migrations
// on startup. All configuration comes from AIRBREW_* environment variables.
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/headercat/airbrew/internal/blob"
	"github.com/headercat/airbrew/internal/bootstrap"
	"github.com/headercat/airbrew/internal/config"
	"github.com/headercat/airbrew/internal/db"
	"github.com/headercat/airbrew/internal/httpserver"
	"github.com/headercat/airbrew/internal/logging"
	"github.com/headercat/airbrew/internal/server"
	"github.com/headercat/airbrew/web"
)

func main() {
	// Load .env / .env.local if present. Real environment variables always win.
	config.LoadFile()

	logger := slog.New(logging.NewColorHandler(os.Stdout, slog.LevelInfo))
	slog.SetDefault(logger)

	cfg, err := config.Load()
	if err != nil {
		logger.Error("config load failed", "error", err)
		os.Exit(1)
	}

	database, err := db.Open(cfg.DatabasePath)
	if err != nil {
		logger.Error("db open failed", "error", err)
		os.Exit(1)
	}
	defer func() {
		if err := database.Close(); err != nil {
			logger.Error("db close failed", "error", err)
		}
	}()

	if err := database.Migrate(context.Background()); err != nil {
		logger.Error("db migrate failed", "error", err)
		os.Exit(1)
	}
	if abs, err := filepath.Abs(cfg.DatabasePath); err == nil {
		logger.Info("database migrated", "path", cfg.DatabasePath, "abs", abs)
	} else {
		logger.Info("database migrated", "path", cfg.DatabasePath)
	}

	if err := bootstrap.EnsureAdmin(context.Background(), database.DB, cfg, logger); err != nil {
		logger.Error("bootstrap admin failed", "error", err)
		os.Exit(1)
	}

	blobs, err := blob.NewLocal(filepath.Join(cfg.DataDir, "files"))
	if err != nil {
		logger.Error("blob store init failed", "error", err)
		os.Exit(1)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	mux := server.Build(server.Deps{
		DB:             database,
		SessionMax:     cfg.SessionMaxAge,
		SessionSecret:  cfg.SessionSecret,
		WebFS:          web.DistFS,
		WebProxyTarget: cfg.WebProxyTarget,
		Blobs:          blobs,
		Ctx:            ctx,
		CookieSecure:   cfg.CookieSecure,
	})

	srv := httpserver.New(httpserver.Deps{
		Addr:           cfg.HTTPAddr,
		Mux:            mux,
		Logger:         logger,
		StrictSecurity: cfg.WebProxyTarget == "", // strict CSP only for the built SPA, not the Vite proxy
	})

	go func() {
		if err := srv.Start(); err != nil {
			logger.Error("http server stopped", "error", err)
			cancel()
		}
	}()

	<-ctx.Done()
	logger.Info("shutdown signal received")
	shutCtx, shutCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer shutCancel()
	if err := srv.Shutdown(shutCtx); err != nil {
		logger.Error("shutdown failed", "error", err)
	}
}
