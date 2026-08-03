// Package server bundles root mux construction so cmd/airbrew stays thin.
package server

import (
	"context"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"

	"github.com/headercat/airbrew/internal/admin"
	"github.com/headercat/airbrew/internal/ai"
	"github.com/headercat/airbrew/internal/ai/agent"
	"github.com/headercat/airbrew/internal/ai/conv"
	aicrypto "github.com/headercat/airbrew/internal/ai/crypto"
	aiprovider "github.com/headercat/airbrew/internal/ai/provider"
	"github.com/headercat/airbrew/internal/audit"
	"github.com/headercat/airbrew/internal/auth"
	"github.com/headercat/airbrew/internal/blob"
	"github.com/headercat/airbrew/internal/chat"
	"github.com/headercat/airbrew/internal/contacts"
	"github.com/headercat/airbrew/internal/db"
	"github.com/headercat/airbrew/internal/drive"
	"github.com/headercat/airbrew/internal/httpserver/middleware"
	"github.com/headercat/airbrew/internal/mail"
	"github.com/headercat/airbrew/internal/modules"
	"github.com/headercat/airbrew/internal/passwords"
	"github.com/headercat/airbrew/internal/workflow"
)

// Deps bundles everything the mux needs.
type Deps struct {
	DB             *db.DB
	SessionMax     time.Duration
	SessionSecret  []byte // HMAC secret used to derive the AI at-rest seal
	WebFS          fs.FS
	WebProxyTarget string
	Blobs          blob.Store
	// Ctx is the process lifecycle context. Modules use it to start background
	// loops (e.g. the vault janitor) that should stop on shutdown.
	Ctx context.Context
	// CookieSecure marks the session cookie Secure (HTTPS-only). Required for
	// production; false for local HTTP dev.
	CookieSecure bool
}

// Build returns the root *http.ServeMux wired with every module.
func Build(d Deps) *http.ServeMux {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	})

	if d.Blobs != nil {
		mux.Handle("/api/files/", filesHandler(d.Blobs))
	}

	// Admin module.
	adminMod := admin.New(d.DB.DB)
	adminMod.RegisterPublicRoutes(mux) // public: GET /api/branding, GET /api/admin/status

	authMod := auth.New(d.DB.DB, auth.Config{SessionMaxAge: d.SessionMax, Blobs: d.Blobs, Audit: adminMod.Audit(), Security: adminMod.Security(), CookieSecure: d.CookieSecure})
	authSub := http.NewServeMux()
	authMod.Handler.RegisterRoutes(authSub)
	mux.Handle("/api/auth/", authMod.SessionMiddleware(authSub))

	adminSub := http.NewServeMux()
	adminMod.RegisterRoutes(adminSub)
	mux.Handle("/api/admin/", authMod.SessionMiddleware(adminSub))

	// Other feature modules. Each registers /api/<name>/status.
	stubState := adminMod.State()

	// Mail module. Status + inbound webhooks are public; mailbox/message/send
	// endpoints require a session; provider config lives under the admin tree.
	mailMod := mail.New(d.DB, stubState, adminMod.Audit(), d.Blobs)
	mailMod.RegisterPublicRoutes(mux) // GET /api/mail/status, POST /api/mail/inbound/{driver}
	mailSub := http.NewServeMux()
	mailMod.RegisterRoutes(mailSub)
	mux.Handle("/api/mail/", authMod.SessionMiddleware(mailSub))
	// Admin provider config: mount under a RequireAdmin-wrapped mux alongside
	// the rest of the admin tree (which is itself SessionMiddleware-wrapped).
	mailAdminSub := http.NewServeMux()
	mailMod.RegisterAdminRoutes(mailAdminSub)
	adminSub.Handle("/api/admin/mail/", admin.RequireAdmin(authMod.UserRepo)(mailAdminSub))
	mailMod.Start(d.Ctx) // inbound poll coordinator

	// Drive module. Status + public share links are public (share routes are
	// rate-limited per IP to blunt password brute-force); file/folder/share
	// management endpoints require a session plus module-enable gating; storage
	// limits live under admin.
	driveMod := drive.New(d.Ctx, d.DB.DB, stubState, adminMod.Audit(), d.Blobs)
	mux.HandleFunc("GET /api/drive/status", driveMod.Status)
	shareLimiter := middleware.NewRateLimiter(30, time.Minute)
	shareSub := http.NewServeMux()
	driveMod.RegisterShareRoutes(shareSub)
	mux.Handle("/api/drive/s/", middleware.RateLimit(shareLimiter, middleware.ClientIPKey)(shareSub))
	driveSub := http.NewServeMux()
	driveMod.RegisterRoutes(driveSub)
	mux.Handle("/api/drive/", authMod.SessionMiddleware(middleware.Chain(driveSub,
		modules.RequireEnabled(stubState, "drive"),
	)))
	driveAdminSub := http.NewServeMux()
	driveMod.RegisterAdminRoutes(driveAdminSub)
	adminSub.Handle("/api/admin/drive/", admin.RequireAdmin(authMod.UserRepo)(driveAdminSub))

	// Contacts (address book) module. Status is public; contact/group CRUD,
	// vCard import/export and avatar uploads require a session, module-enable
	// gating, and a per-IP rate limit (import parses up to 16 MiB).
	contactsMod := contacts.New(d.Ctx, d.DB.DB, stubState, adminMod.Audit(), d.Blobs)
	contactsMod.RegisterPublicRoutes(mux)
	contactsSub := http.NewServeMux()
	contactsMod.RegisterRoutes(contactsSub)
	contactsLimiter := middleware.NewRateLimiter(120, time.Minute)
	mux.Handle("/api/contacts/", authMod.SessionMiddleware(middleware.Chain(contactsSub,
		modules.RequireEnabled(stubState, "contacts"),
		middleware.RateLimit(contactsLimiter, middleware.ClientIPKey),
	)))

	// Chat module. Status is public; room/message/user discovery endpoints
	// require a session, module-enable gating, and a modest per-IP rate limit.
	chatMod := chat.New(d.DB.DB, stubState, adminMod.Audit())
	chatMod.RegisterPublicRoutes(mux)
	chatSub := http.NewServeMux()
	chatMod.RegisterRoutes(chatSub)
	chatLimiter := middleware.NewRateLimiter(120, time.Minute)
	mux.Handle("/api/chat/", authMod.SessionMiddleware(middleware.Chain(chatSub,
		modules.RequireEnabled(stubState, "chat"),
		middleware.RateLimit(chatLimiter, middleware.ClientIPKey),
	)))
	// Workflow automation. Status + webhook triggers are public; authoring,
	// manual runs and run history require a session plus module-enable gating.
	workflowMod := workflow.New(d.DB.DB, stubState, adminMod.Audit(), mailMod)
	workflowMod.RegisterPublicRoutes(mux)
	workflowSub := http.NewServeMux()
	workflowMod.RegisterRoutes(workflowSub)
	mux.Handle("/api/workflow/", authMod.SessionMiddleware(middleware.Chain(workflowSub,
		modules.RequireEnabled(stubState, "workflow"),
	)))
	workflowMod.Start(d.Ctx)

	// AI agent module. Status is public; user endpoints require a session,
	// module-enable gating, and a per-IP rate limit. Admin provider/agent
	// endpoints live under the RequireAdmin tree alongside the other
	// per-module admin routes.
	aiMod := buildAIModule(d, stubState, adminMod.Audit())
	aiMod.RegisterPublicRoutes(mux)
	aiSub := http.NewServeMux()
	aiMod.RegisterUserRoutes(aiSub)
	aiLimiter := middleware.NewRateLimiter(30, time.Minute)
	mux.Handle("/api/ai/", authMod.SessionMiddleware(middleware.Chain(aiSub,
		modules.RequireEnabled(stubState, "ai"),
		middleware.RateLimit(aiLimiter, middleware.ClientIPKey),
	)))
	aiAdminSub := http.NewServeMux()
	aiMod.RegisterAdminRoutes(aiAdminSub)
	adminSub.Handle("/api/admin/ai/", admin.RequireAdmin(authMod.UserRepo)(aiAdminSub))

	// Password vault. Status is public; the remaining endpoints require a
	// session, so they are mounted on a sub-mux wrapped in SessionMiddleware.
	// The sub-mux additionally enforces module-disable gating (so an admin can
	// actually take the vault offline) and a per-IP rate limit.
	pwMod := passwords.New(d.Ctx, d.DB.DB, stubState, adminMod.Audit(), d.Blobs)
	mux.HandleFunc("GET /api/vault/status", pwMod.Status)
	pwSub := http.NewServeMux()
	pwMod.RegisterRoutes(pwSub)
	vaultLimiter := middleware.NewRateLimiter(120, time.Minute)
	mux.Handle("/api/vault/", authMod.SessionMiddleware(middleware.Chain(pwSub,
		modules.RequireEnabled(stubState, "passwords"),
		middleware.RateLimit(vaultLimiter, middleware.ClientIPKey),
	)))

	if d.WebProxyTarget != "" {
		mux.Handle("/", webProxyHandler(d.WebProxyTarget))
	} else {
		mux.Handle("/", spaHandler(d.WebFS))
	}

	return mux
}

// buildAIModule wires the AI module's dependencies: an at-rest seal for
// provider API keys derived from the session secret, the provider
// repository, the conversation + agent repos, the tool registry (with
// built-ins), and the runtime with its provider resolver. Seeding of the
// built-in agents is best-effort and never aborts startup.
func buildAIModule(d Deps, state *modules.State, auditSvc *audit.Service) *ai.Module {
	seal, err := aicrypto.New(d.SessionSecret)
	if err != nil {
		slog.Default().Error("ai: seal init failed; provider keys stored unencrypted",
			"error", err)
		seal = nil
	}
	provRepo := aiprovider.NewRepository(d.DB.DB, seal)
	convRepo := conv.NewRepository(d.DB.DB)
	convSvc := conv.NewService(convRepo)
	agentsRepo := agent.NewDefinitionRepo(d.DB.DB)
	tools := agent.NewToolRegistry()
	agent.Builtin(tools)

	resolve := func(ctx context.Context) (aiprovider.LLMClient, error) {
		return provRepo.Resolve(ctx, aiprovider.DirectionChat)
	}
	m := ai.New(state, provRepo, convSvc, agentsRepo, tools, resolve, auditSvc)
	if err := m.Seed(context.Background()); err != nil {
		slog.Default().Warn("ai: seed built-in agents failed", "error", err)
	}
	return m
}

// filesHandler serves files from a blob.Store at /api/files/<namespace>/<name>.
func filesHandler(store blob.Store) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rel := strings.TrimPrefix(r.URL.Path, "/api/files/")
		if rel == "" || strings.Contains(rel, "..") {
			http.NotFound(w, r)
			return
		}
		body, ct, err := store.Open(r.Context(), rel)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		defer body.Close()
		w.Header().Set("Content-Type", ct)
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		if _, err := io.Copy(w, body); err != nil {
			return
		}
	})
}

// spaHandler serves embedded files, returning index.html for unknown paths so
// client-side routing works (history API).
func spaHandler(webFS fs.FS) http.Handler {
	fileServer := http.FileServerFS(webFS)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		if path == "/" {
			fileServer.ServeHTTP(w, r)
			return
		}
		if _, err := fs.Stat(webFS, path[1:]); err != nil {
			r2 := r.Clone(r.Context())
			r2.URL.Path = "/"
			fileServer.ServeHTTP(w, r2)
			return
		}
		fileServer.ServeHTTP(w, r)
	})
}

func webProxyHandler(target string) http.Handler {
	u, err := url.Parse(target)
	if err != nil {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "invalid AIRBREW_WEB_PROXY_TARGET", http.StatusInternalServerError)
		})
	}
	return httputil.NewSingleHostReverseProxy(u)
}
