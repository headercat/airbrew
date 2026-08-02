# Airbrew

Airbrew is a single-tenant personal-cloud workspace shipped as one Go binary.
It bundles an OAuth 2.1 / OpenID Connect provider with productivity surfaces
(mail, drive, contacts, chat, AI agents, automation workflows). The binary
embeds its React SPA, so deployment is one file + one SQLite database.

## Status

| Module     | State                                  |
| ---------- | -------------------------------------- |
| **Auth**   | Milestone 1 (users/sessions/password)  |
| OAuth/OIDC | Schema ready, endpoints not yet built  |
| Mail       | Stub                                   |
| Drive      | Stub                                   |
| Contacts   | Stub                                   |
| Chat       | Stub                                   |
| AI         | Stub                                   |
| Workflow   | Stub                                   |

See `docs/auth.md` for the milestone roadmap.

## Quick start

Requirements:

- Go 1.22+ (built with Go 1.26)
- Node.js 20+ (only for SPA development)

```sh
# Build SPA and Go binary
make build

# Run
AIRBREW_HTTP_ADDR=:5050 AIRBREW_SESSION_SECRET=$(openssl rand -hex 32) ./bin/airbrew

# Health check
curl localhost:5050/healthz   # -> {"ok":true}

# Register + login + me
curl -X POST localhost:5050/api/auth/register \
  -H 'content-type: application/json' \
  -d '{"email":"alice@example.com","password":"hunter2hunter2"}'
```

## Development

```sh
# Run API and Vite dev servers
make dev
# SPA at http://localhost:5051
```

`make dev:api` rebuilds and restarts the Go API when backend files change.
`make dev:web` runs Vite HMR. Use either command to run one side separately.

To build only the SPA into `web/dist/` so `make build:api` serves it from the binary:

```sh
make install:web
make build:web
```

## Configuration

All knobs are environment variables prefixed with `AIRBREW_`:

| Variable                  | Default (dev)                       | Notes                                    |
| ------------------------- | ----------------------------------- | ---------------------------------------- |
| `AIRBREW_HTTP_ADDR`       | `:5050`                             | Listen address                           |
| `AIRBREW_DATABASE_PATH`   | `./airbrew.db`                      | SQLite file path                         |
| `AIRBREW_PUBLIC_URL`      | `http://localhost:5050`             | Issuer / SPA base URL                    |
| `AIRBREW_SESSION_SECRET`  | dev default (32+ bytes required)    | HMAC secret for session cookies          |
| `AIRBREW_SESSION_MAX_AGE` | `336h` (14 days)                    | Session lifetime                         |
| `AIRBREW_LOG_LEVEL`       | `info`                              | slog level                               |
| `AIRBREW_BOOTSTRAP_ADMIN_EMAIL` | `admin@airbrew.local`        | First-run admin email                    |
| `AIRBREW_BOOTSTRAP_ADMIN_PASSWORD` | random generated password | Optional fixed first-run admin password  |

On startup, if no admin user exists, Airbrew creates one and prints the
credentials to stdout. A generated password is shown only once.

## Layout

```
cmd/airbrew/       Single entry point
internal/
  config/          Env-based configuration
  db/              SQLite open + embedded SQL migrations
  id/              nanoid-style ID generator
  httpserver/      Server bootstrap + middleware (requestid, recover, access log)
  auth/            Auth module (OAuth/OIDC provider)
    user/          User domain, repository, service
    password/      argon2id hashing
    session/       Browser session lifecycle
    oauth/         Reserved for OAuth/OIDC endpoints
    handler/       JSON HTTP handlers
  mail/            Stub
  drive/           Stub
  contacts/        Stub
  chat/            Stub
  ai/              Stub
  workflow/        Stub
  server/          Root mux wiring all modules
web/               Vite + React SPA (embedded via go:embed)
docs/              Architecture and feature design docs
```

## Design constraints

- **Single binary**: everything ships in one executable.
- **CGO-free**: `modernc.org/sqlite` (pure Go) so cross-compilation just works.
- **Standard library first**: `net/http` (Go 1.22+ pattern routing), `log/slog`,
  `crypto/rand`, `database/sql`. Only essential external deps: `argon2`.
- **Application-generated IDs**: 21-char nanoid-style for all primary keys.
- **Single-tenant**: no `workspace_id` columns; one SQLite file per deployment.

## License

Proprietary.
