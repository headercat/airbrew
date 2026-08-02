# Architecture

## Goals

1. **Single executable.** No Docker required, no external services. The binary
   embeds the SPA, the migrations, and serves the JSON API. Only a writable
   filesystem path is needed for the SQLite database.
2. **CGO-free.** `modernc.org/sqlite` is a pure-Go transpilation of SQLite, so
   `GOOS=linux GOARCH=arm64 go build` works from any host without a C toolchain.
3. **Stdlib-first.** Go 1.22+ pattern routing in `net/http`, structured logging
   via `log/slog`, configuration via `os.Getenv`. The only intentional external
   dependency is `golang.org/x/crypto/argon2` for password hashing.
4. **Module-per-concern.** Each functional surface (auth, mail, drive, contacts,
   chat, ai, workflow) is its own `internal/` package exposing a `Module` type
   with `RegisterRoutes(*http.ServeMux)`. Composition happens in
   `internal/server`.

## Request flow

```
HTTP request
  → httpserver.New() middleware chain
    → RequestID  : attach X-Request-ID
    → Recover    : catch panics, log stack
    → AccessLog  : one structured line per request
  → auth.SessionMiddleware (for /api/auth/*)
    → loads session from cookie if present
  → mux dispatch
    → /healthz
    → /api/auth/{register,login,logout,me}
    → /api/{mail,drive,contacts,chat,ai,workflow}/status
    → /  (embedded SPA, SPA fallback for client-side routes)
```

## Data model

All schema lives in `internal/db/migrations/*.sql`. The initial migration
creates 12 tables for the auth module and reserves indexes/FKs that later
milestones will rely on:

- `users`, `password_credentials`, `sessions` — covered by Milestone 1
- `oauth_clients`, `oauth_client_redirect_uris`,
  `oauth_client_post_logout_redirect_uris` — Milestone 2
- `oauth_authorization_codes` — Milestones 3–5
- `oauth_refresh_tokens` (with `family_id`, `parent_token_id`,
  `replaced_by_token_id`) — Milestones 8
- `oauth_consents` — Milestone 4
- `oauth_signing_keys` — Milestones 6–7
- `audit_logs`, `server_settings` — cross-cutting

Time columns use SQLite's `DATETIME` declared type. The actual storage is TEXT
because SQLite has no datetime type — but the declared type is what
`modernc.org/sqlite` keys on to auto-parse rows into Go `time.Time` on scan.
Write format is configured via `_time_format=sqlite` in the DSN.

## Identifier policy

- All primary keys: 21-char nanoid-style string (`internal/id`), URL-safe
  alphabet, 6 bits per byte (no modular bias because alphabet size is 64).
- Session tokens / OAuth codes / refresh tokens / client secrets: 32 random
  bytes, base64url-encoded. Stored as SHA-256 hash only.
- Users have two unrelated identifiers:
  - `users.id` — internal FK target, never exposed to OAuth clients
  - `users.public_subject` — used as the OIDC `sub` claim in tokens

## Password storage

argon2id, encoded in PHC format (`$argon2id$v=19$m=65536,t=3,p=2$<salt>$<hash>`).
Parameters: 64 MiB memory, 3 iterations, parallelism 2, 32-byte key, 16-byte
salt. Verification parses the encoded string and runs the derivation with the
recorded parameters, so future stronger settings can be introduced without
invalidating existing hashes.

## Session lifecycle

1. `POST /api/auth/login` verifies credentials; on success `session.Service`
   issues a 32-byte random token, persists `SHA-256(token)` to `sessions`,
   returns the raw token in an HttpOnly cookie.
2. The session middleware on subsequent requests looks up the cookie, hashes
   it, fetches the row, checks expiry and `revoked_at`.
3. `POST /api/auth/logout` marks the row `revoked_at = now` and clears the
   cookie.
4. Compromise response: rotate `AIRBREW_SESSION_SECRET` and `UPDATE sessions
   SET revoked_at = now` — the hash makes the cookie value useless without the
   DB.

## Frontend

The React SPA lives in `web/` and is built with Vite. `make build:web` writes
artifacts to `web/dist/`, which `web/embed.go` embeds via `go:embed`. In
development, Vite runs on :5051 and proxies `/api` to :5050 so changes hot-
reload without rebuilding the Go binary.

For unknown filesystem paths (e.g. `/login`), the SPA handler falls back to
`index.html` so client-side routing works in production.

## Why not split into separate binaries?

Because the whole product is one user, one machine, one workspace. Multi-
process deployment would trade the single-binary deployment story for no real
isolation benefit. If a future module needs long-running workers (e.g. AI
agents) they will run as goroutines inside the same process, with graceful
shutdown coordinated by `signal.NotifyContext`.

## Why SQLite over Postgres?

Because the deployment target is a single tenant. SQLite with WAL handles the
write rate of a single user easily, removes a runtime dependency, and lets the
whole product live in one file. `SetMaxOpenConns(1)` is set defensively so
write transactions cannot deadlock under the modernc driver; if this ever
becomes a bottleneck we will switch to a connection-per-role pattern (separate
read and write pools) rather than changing the database.
