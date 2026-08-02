# Admin Console Design

Reference analysis of Google Workspace Admin Console mapped to Airbrew's
single-tenant, self-hosted architecture. Each section lists the Google
Workspace analogue, what Airbrew adopts, what it defers, and the data-model
/API impact.

---

## Design principles

Airbrew is **single-tenant and self-hosted**. This changes the scope from
Google Workspace in three ways:

| Google Workspace            | Airbrew                          |
| --------------------------- | -------------------------------- |
| Multi-tenant SaaS, billed per seat | Single workspace, no billing |
| Org-unit tree for policy scoping | Flat workspace, group-based scoping |
| Device management, MDM      | Session tracking only           |
| Marketplace, resellers      | Self-contained, no marketplace  |

What we **keep**: dashboard, users, groups, security center, audit logs,
OAuth client management, branding, module configuration.

What we **defer**: billing, MDM, BigQuery export, CSE, DLP engine,
Marketplace, SCIM/LDAP sync, multi-domain.

---

## Module map (sidebar IA)

```
Admin
├── Dashboard            ← system health + quick stats
├── Users                ← accounts, roles, status
├── Groups               ← (phase 2) shared mailboxes, access targets
├── Security             ← sessions, password policy, login history
├── Modules              ← enable/disable + per-module config
├── OAuth Clients        ← (phase 2) OAuth 2.1 client registry
├── Audit Log            ← admin actions, auth events, module events
├── Branding             ← workspace name, logo, colors
└── System               ← backup/export, env info, feature flags
```

---

## Phase 1 — MVP (already built or building)

### Dashboard

**Google analogue:** Admin console home with task cards, quick actions.

**Airbrew scope:**
- Workspace summary: total users, active sessions, module count
- Module health grid (from `/api/<module>/status`)
- Recent admin actions (last 10 audit log entries)
- Bootstrap status: admin exists? DB location? Data dir size?

**Data:** `users`, `sessions`, `audit_logs`, `server_settings`.

### Users

**Already implemented:** list, role toggle (user ↔ admin), status toggle
(active ↔ suspended). Self-lockout guards in place.

**Phase 1 additions:**
- Search by email/display name
- Create user (admin-initiated, not self-register)
- Reset password (admin generates temp password, printed once)
- View user detail: profile fields, sessions, recent activity
- Soft-delete with recovery window (30 days)

**Data model:** `users` already has `status='deleted'`; add `deleted_at`
to distinguish soft-delete timestamp from hard purge. `password_credentials`
already has the hash; add a `password_changed_at` column.

### Modules

**Already implemented:** enable/disable toggle, status endpoint reflects
state.

**Phase 1 additions:**
- Per-module configuration form (schema-driven from a `ModuleConfig` interface
  each module can implement)
- Module health check (not just enabled/disabled, but "can connect to
  upstream SMTP?", "has API key?", etc.)

**Data model:** `server_settings` key-value store. Config stored as
`module.<key>.config` (JSON string).

### Audit Log

**Google analogue:** Reports → Audit & Investigation tool.

**Airbrew scope:**
- Append-only `audit_logs` table already exists
- Surface in admin UI: filterable by event_type, actor, date range
- Event types to emit:
  - `user.created`, `user.updated`, `user.suspended`, `user.deleted`
  - `user.role_changed`
  - `user.password_reset`, `user.password_changed`
  - `session.login`, `session.logout`, `session.revoked`
  - `module.enabled`, `module.disabled`, `module.config_changed`
  - `admin.bootstrap`
  - `oauth.client_created`, `oauth.token_issued`, `oauth.token_revoked`
  - `profile.updated`, `avatar.uploaded`

**Data model:** `audit_logs` table already has the right columns. Add a
`before` and `after` JSON column for diffing changes.

---

## Phase 2 — Security & Identity

### Security Center

**Google analogue:** Security → Security Center (dashboard + investigation).

**Airbrew scope:**
- **Active sessions:** list all non-expired, non-revoked sessions with IP,
  user-agent, last-seen. Admin can revoke any session.
- **Login history:** recent logins (success + failure) with IP, UA,
  timestamp. Failed-login rate alerting.
- **Password policy:** configurable min length (8-128), require
  uppercase/lowercase/digit/symbol, max age (force rotation), history
  (prevent reuse of last N).
- **Session policy:** max concurrent sessions per user, idle timeout,
  IP allowlist (optional).

**Data model:**
```sql
-- Password policy stored in server_settings as JSON
-- Key: security.password_policy
-- Value: {"min_length":12,"require_uppercase":true,...}

-- Login attempts (extends audit_logs or dedicated table)
CREATE TABLE login_attempts (
  id TEXT PRIMARY KEY,
  user_id TEXT,           -- nullable (unknown email attempts)
  email TEXT,             -- what was typed
  success INTEGER NOT NULL,
  ip_address TEXT,
  user_agent TEXT,
  created_at DATETIME NOT NULL
);
CREATE INDEX idx_login_attempts_user ON login_attempts(user_id);
CREATE INDEX idx_login_attempts_created ON login_attempts(created_at);
```

### OAuth Clients (from auth-provider-plan.md)

**Google analogue:** Apps → SAML apps + OAuth consent + domain-wide delegation.

**Airbrew scope:** Milestone 2 from the auth roadmap:
- Register OAuth 2.1 clients (public/confidential)
- Manage redirect URIs (exact match)
- Manage allowed scopes
- View issued tokens (access + refresh)
- Revoke tokens (per client, per user, per family)

**Data model:** `oauth_clients`, `oauth_client_redirect_uris`,
`oauth_authorization_codes`, `oauth_refresh_tokens`, `oauth_consents` — all
already in migration `0001_initial.sql`.

---

## Phase 3 — Groups & Sharing

### Groups

**Google analogue:** Groups (mailing lists, collaborative inboxes, policy targets).

**Airbrew scope:**
- Create groups (mailing-list style)
- Add/remove members
- Use groups as:
  - Mail distribution lists (when mail module is built)
  - Drive sharing targets (when drive module is built)
  - Permission scopes (e.g., "AI Agents" module accessible to group X)

**Data model:**
```sql
CREATE TABLE groups (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  slug TEXT NOT NULL UNIQUE,
  description TEXT,
  created_at DATETIME NOT NULL,
  updated_at DATETIME NOT NULL
);

CREATE TABLE group_members (
  id TEXT PRIMARY KEY,
  group_id TEXT NOT NULL,
  user_id TEXT NOT NULL,
  role TEXT NOT NULL DEFAULT 'member'
    CHECK (role IN ('owner', 'manager', 'member')),
  created_at DATETIME NOT NULL,
  UNIQUE (group_id, user_id),
  FOREIGN KEY (group_id) REFERENCES groups(id) ON DELETE CASCADE,
  FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
);
```

---

## Phase 4 — Branding & System

### Branding

**Google analogue:** Account → Company profile, custom logos, BIMI.

**Airbrew scope:**
- Workspace name (shown in sidebar, browser title, emails)
- Logo upload (replaces Coffee icon)
- Primary color (CSS variable override)
- Email from-name/address (when mail module exists)
- Custom CSS (advanced, optional)

**Data model:** `server_settings` key-value:
- `branding.name`
- `branding.logo_url`
- `branding.primary_color` (HSL tuple)
- `branding.custom_css`

### System

**Google analogue:** not directly analogous (closest: Account → Profile).

**Airbrew scope:**
- Instance info: version, Go version, DB size, data dir path, uptime
- Backup: download DB snapshot (SQLite `VACUUM INTO`)
- Export: full workspace export (DB + files as tar.gz)
- Feature flags: toggle experimental features
- Env info: show current config (secrets masked)

**Data model:** no schema change; uses runtime info + `server_settings` for
feature flags.

---

## API surface (target)

All under `/api/admin/*`, require admin role.

### Users
```
GET    /api/admin/users?search=&limit=&offset=   list (paginated)
POST   /api/admin/users                           create user
GET    /api/admin/users/{id}                      detail
PATCH  /api/admin/users/{id}                      update role/status
DELETE /api/admin/users/{id}                      soft-delete
POST   /api/admin/users/{id}/reset-password       generate temp password
GET    /api/admin/users/{id}/sessions             active sessions
DELETE /api/admin/users/{id}/sessions             revoke all sessions
GET    /api/admin/users/{id}/activity             recent audit events
```

### Groups (phase 3)
```
GET    /api/admin/groups
POST   /api/admin/groups
GET    /api/admin/groups/{id}
PATCH  /api/admin/groups/{id}
DELETE /api/admin/groups/{id}
POST   /api/admin/groups/{id}/members
DELETE /api/admin/groups/{id}/members/{userId}
```

### Security
```
GET    /api/admin/security/sessions               all active sessions
DELETE /api/admin/security/sessions/{id}          revoke session
GET    /api/admin/security/login-history          recent attempts
GET    /api/admin/security/password-policy
PUT    /api/admin/security/password-policy
GET    /api/admin/security/ip-allowlist
PUT    /api/admin/security/ip-allowlist
```

### Modules
```
GET    /api/admin/modules                         list + enabled + config
PATCH  /api/admin/modules/{key}                   toggle enabled
GET    /api/admin/modules/{key}/config            per-module config schema + values
PUT    /api/admin/modules/{key}/config            update config
GET    /api/admin/modules/{key}/health            deep health check
```

### OAuth Clients (phase 2)
```
GET    /api/admin/oauth/clients
POST   /api/admin/oauth/clients
GET    /api/admin/oauth/clients/{id}
PATCH  /api/admin/oauth/clients/{id}
DELETE /api/admin/oauth/clients/{id}
GET    /api/admin/oauth/clients/{id}/tokens       issued tokens
DELETE /api/admin/oauth/tokens/{id}               revoke specific token
```

### Audit Log
```
GET    /api/admin/audit?event_type=&actor=&from=&to=&limit=&offset=
```

### Branding
```
GET    /api/admin/branding
PUT    /api/admin/branding
POST   /api/admin/branding/logo                   upload logo
```

### System
```
GET    /api/admin/system/info                     version, paths, sizes
POST   /api/admin/system/backup                   trigger DB snapshot
GET    /api/admin/system/feature-flags
PUT    /api/admin/system/feature-flags
```

---

## Migration plan

| Migration | Adds                                              | Phase |
| --------- | ------------------------------------------------- | ----- |
| 0004      | `users.deleted_at`, `password_credentials.password_changed_at` | 1 |
| 0005      | `login_attempts` table                            | 2     |
| 0006      | `groups`, `group_members`                         | 3     |

No migration needed for branding/modules — those use `server_settings`.

---

## What Airbrew deliberately omits (vs. Google Workspace)

| Feature                        | Why omitted                          |
| ------------------------------ | ------------------------------------ |
| Billing & licensing            | Self-hosted, no per-seat billing     |
| Organizational units (tree)    | Single workspace, flat structure     |
| Device management (MDM/MAM)    | Not a mobile device manager          |
| Client-side encryption (CSE)   | Encryption-at-rest is OS-level       |
| Data Loss Prevention (DLP)     | Too complex for single-tenant MVP    |
| Context-Aware Access           | Defer to phase 5+ (IP allowlist is the MVP) |
| Marketplace / app store        | Self-contained                      |
| BigQuery export                | Audit log API is sufficient          |
| SCIM / LDAP sync               | Single workspace, manual/API create  |
| Multi-domain                   | Single domain per workspace          |
| Chrome Enterprise              | Not a browser vendor                 |
