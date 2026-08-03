# Mail module

A per-user mailbox with pluggable inbound (receive) and outbound (send) drivers.
Each user owns one or more mailboxes (an email address each). An administrator
selects exactly one inbound driver and one outbound driver as "active"; the
active driver is resolved at runtime from the `mail_providers` table. The same
binary ships every driver, so switching providers is a config change, not a
redeploy.

## Driver catalog

### Inbound (receive)

| Driver  | Mechanism                                                       |
| ------- | --------------------------------------------------------------- |
| `cloudflare` | Cloudflare Email Routing → Worker → `POST /api/mail/inbound/cloudflare` (raw RFC822 + bearer secret) |
| `ses`        | AWS SES inbound → Lambda → `POST /api/mail/inbound/ses` (raw RFC822 + bearer secret) |
| `imap`       | Background poller logs into an IMAP server and ingests new messages via `BODY.PEEK[]` |
| `pop3`       | Background poller logs into a POP3 server and ingests messages not yet seen (tracked by `UIDL`) |

Cloudflare and SES are **push** drivers: the external provider calls into
Airbrew over HTTP. The webhook is public (no browser session) but authenticated
by a shared bearer secret stored in the provider config. The Worker / Lambda
code is operator-supplied; this document defines the webhook contract:

```
POST /api/mail/inbound/<driver>
Authorization: Bearer <provider secret>
Content-Type: text/plain            # or application/json; see driver notes
X-Airbrew-Mailbox: <recipient address>   # envelope recipient (routing key)

<raw RFC822 message bytes>
```

IMAP and POP3 are **poll** drivers: a coordinator goroutine, started by the
Module with the process lifetime context, reads the active inbound provider
every ~30s and runs the matching poller. When the active provider changes, the
coordinator stops the old poll loop and starts the new one.

### Outbound (send)

| Driver      | Mechanism                                                    |
| ----------- | ------------------------------------------------------------ |
| `sendgrid`  | `POST https://api.sendgrid.com/v3/mail/send` (Bearer API key) |
| `mailgun`   | `POST https://api.mailgun.net/v3/<domain>/messages` (Basic auth API key) |
| `ncloud`    | `POST https://mail.api.ncloud.com/...` (Ncloud SENS, access/secret key) |
| `ses`       | AWS SES `SendRawEmail` via the POST `/aws/api/email` Query protocol |
| `cloudflare`| `POST <worker URL>` — operator's Worker forwards to MailChannels |
| `smtp`      | `net/smtp.SendMail` (plain or `STARTTLS`, optional auth) |

Outbound drivers are pure stdlib `net/http` / `net/smtp` clients. They accept a
`letter.Outgoing` (already-validated sender/recipients/subject/text/html) and
format it for their transport.

## Routing

A mailbox is addressed by its lowercase `address` (`local@domain`). Inbound
ingest looks the mailbox up by recipient address; messages for unknown addresses
are dropped with a warning (single-tenant: every address must be provisioned).
Outbound mail is always sent from one of the user's own mailbox addresses.

## Data model

A new migration `0006_mail.sql` mirrors the style of `0001_initial.sql`. All PKs
use `internal/id`; `DATETIME` defaults follow existing convention. Recipient
address lists are stored as JSON arrays of `{name,address}` objects.

The active provider per direction is enforced unique via a partial index so at
most one inbound and one outbound driver is active at a time.

## Module layout

`internal/mail/` mirroring `internal/auth/` and `internal/passwords/`:

```
internal/mail/
  mail.go            Module wiring (New, RegisterRoutes, Start)
  letter/            shared MIME helpers: Address, Outgoing, BuildRFC822, Parse
  inbox/             Mailbox + Message domain, repository, service (ingest/list/send)
  provider/          mail_providers persistence + Service (active driver resolution)
  inbound/           Inbounder contract + webhook + poll drivers (cloudflare, ses, imap, pop3)
  outbound/          Outbounder contract + send drivers (sendgrid, mailgun, ncloud, ses, cloudflare, smtp)
  handler/           JSON HTTP handlers (mailboxes, messages, send, admin provider config)
```

## Endpoint inventory

```
# Public status (no session)
GET  /api/mail/status

# Inbound webhooks (public, bearer-authenticated by provider secret)
POST /api/mail/inbound/cloudflare
POST /api/mail/inbound/ses

# Authenticated user endpoints (session required)
GET    /api/mail/mailboxes
POST   /api/mail/mailboxes
DELETE /api/mail/mailboxes/{id}
GET    /api/mail/counts?mailbox=                # inbox/sent/draft/starred/unread totals
GET    /api/mail/messages?mailbox=&folder=&q=&thread=&limit=&offset=
GET    /api/mail/messages/{id}
PATCH  /api/mail/messages/{id}                   (read/unread/star/draft flags)
DELETE /api/mail/messages/{id}
GET    /api/mail/messages/{id}/raw               (download RFC822)
POST   /api/mail/send                            (compose + send)
POST   /api/mail/drafts                          (save draft; send=true sends immediately)
PATCH  /api/mail/drafts/{id}                     (update draft; send=true sends it)
DELETE /api/mail/drafts/{id}
POST   /api/mail/attachments                     (pending outbound upload)
GET    /api/mail/attachments/{id}                (download)
DELETE /api/mail/attachments/{id}                (pending only)

# Admin endpoints (admin session required)
GET    /api/admin/mail/providers
PUT    /api/admin/mail/providers                 (upsert + activate a driver config)
DELETE /api/admin/mail/providers/{id}
POST   /api/admin/mail/providers/{id}/test       (sends a probe email / runs one poll)
```

### Query and folders

`GET /api/mail/messages` accepts:

- `mailbox` — restrict to one mailbox id
- `folder` — `inbox` (default), `sent`, `draft`, `starred`, `unread`
- `q` — free-text search across subject, from, to/cc and body (case-insensitive
  LIKE; user-supplied `%` and `_` are escaped so they match literally)
- `thread` — restrict to one conversation id
- `limit`/`offset` — pagination; limit defaults to 50 and caps at 200

`GET /api/mail/counts` returns a single JSON object with `inbox`, `sent`,
`draft`, `starred` and `unread` totals for the user (optionally scoped to one
mailbox) so the SPA can render folder badges in one round-trip.

### Drafts

Drafts are stored as `mail_messages` rows with `direction='outbound'` and
`is_draft=1`. A draft has no `message_id` until it is sent; its `thread_id`
defaults to its own row id so unsent drafts form their own conversations. The
`POST` and `PATCH` draft endpoints both accept `send: true` as a shortcut:
the draft is saved (or updated) and then immediately handed to the active
outbound driver in one round-trip. Attachment ids supplied on draft save are
rebound to the draft (removing dropped ones back to the pending pool).

## Milestone roadmap

| #  | Milestone                                                       |
| -- | --------------------------------------------------------------- |
| 1  | Schema `0006_mail.sql`, letter helpers, mailbox CRUD            |
| 2  | Ingest pipeline (parse RFC822 → store), message list/read       |
| 3  | Outbound driver interface + all six send drivers + `/send`     |
| 4  | Inbound webhook drivers (cloudflare, ses)                       |
| 5  | Poll drivers (pop3 stdlib client, imap via go-imap) + coordinator |
| 6  | Admin provider config endpoints + active-driver resolution     |
| 7  | SPA mailbox UI                                                  |

## Decisions

- **go-imap dependency.** IMAP is a complex protocol that cannot be implemented
  reasonably in the standard library, so `github.com/emersion/go-imap` is added
  as an essential dependency, analogous to `golang.org/x/crypto/argon2` being
  the one crypto dep. POP3 is simple enough to ship as a pure-stdlib client.
- **Secrets at rest.** Single-tenant with the DB on disk; provider credentials
  are stored as JSON in `mail_providers.config`. An envelope-encryption layer is
  reserved for a later milestone if multi-tenant or hosted deployments are added.
- **Bounce/complaint handling.** Out of MVP scope; the SES webhook only ingests
  inbound mail, not bounce notifications.
- **Poll backoff.** The inbound coordinator backs off exponentially on
  consecutive poll failures (capped at 10x the base interval) so a misconfigured
  server or transient outage does not produce a tight error loop. A successful
  poll resets the failure counter.
- **Search.** A case-insensitive LIKE scan over subject/from/to/cc/body keeps
  the implementation SQLite-native without pulling in FTS5. User-supplied `%`
  and `_` are escaped so a search for a literal value is exact.
- **HTML sanitization.** Inbound HTML bodies are rendered in a sandboxed iframe
  (`sandbox=""`) by the SPA, and additionally sanitized client-side to strip
  scripts, forms, media and unsafe URL schemes before the iframe srcDoc is set.
  This is defence in depth; the sandbox is the primary boundary.
