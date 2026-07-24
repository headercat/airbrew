-- 0001_initial.sql
-- Initial schema for Airbrew: auth (users/sessions), OAuth/OIDC provider,
-- and reserved tables for future mail/drive/contacts/chat/ai/workflow modules.
--
-- All primary keys are application-generated 21-char IDs (see internal/id).
-- Time columns use SQLite DATETIME declared type so modernc.org/sqlite parses
-- them into Go time.Time on scan; underlying storage is TEXT (RFC3339-ish).

PRAGMA foreign_keys = ON;

-- -----------------------------------------------------------------------------
-- Server-level key/value settings
-- -----------------------------------------------------------------------------
CREATE TABLE server_settings (
  key        TEXT PRIMARY KEY NOT NULL,
  value      TEXT NOT NULL,
  updated_at DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now'))
);

-- -----------------------------------------------------------------------------
-- Users (separate public_subject is the OIDC 'sub')
-- -----------------------------------------------------------------------------
CREATE TABLE users (
  id             TEXT PRIMARY KEY NOT NULL,
  public_subject TEXT NOT NULL UNIQUE,
  email          TEXT NOT NULL UNIQUE COLLATE NOCASE,
  status         TEXT NOT NULL DEFAULT 'active'
                 CHECK (status IN ('active', 'suspended', 'deleted')),
  email_verified INTEGER NOT NULL DEFAULT 0,
  display_name   TEXT,
  avatar_url     TEXT,
  created_at     DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
  updated_at     DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now'))
);
CREATE INDEX idx_users_status ON users(status);

-- -----------------------------------------------------------------------------
-- Password credentials (1:1 with users, argon2id)
-- -----------------------------------------------------------------------------
CREATE TABLE password_credentials (
  user_id       TEXT PRIMARY KEY NOT NULL,
  password_hash TEXT NOT NULL,
  password_alg  TEXT NOT NULL DEFAULT 'argon2id',
  created_at    DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
  updated_at    DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
  FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
);

-- -----------------------------------------------------------------------------
-- Browser login sessions (server stores SHA-256 of cookie token)
-- -----------------------------------------------------------------------------
CREATE TABLE sessions (
  id                 TEXT PRIMARY KEY NOT NULL,
  user_id            TEXT NOT NULL,
  session_token_hash TEXT NOT NULL UNIQUE,
  ip_address         TEXT,
  user_agent         TEXT,
  created_at         DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
  expires_at         DATETIME NOT NULL,
  revoked_at         DATETIME,
  FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
);
CREATE INDEX idx_sessions_user_id    ON sessions(user_id);
CREATE INDEX idx_sessions_expires_at ON sessions(expires_at);

-- -----------------------------------------------------------------------------
-- OAuth 2.1 / OIDC: registered clients
-- -----------------------------------------------------------------------------
CREATE TABLE oauth_clients (
  id                          TEXT PRIMARY KEY NOT NULL,
  client_id                   TEXT NOT NULL UNIQUE,
  name                        TEXT NOT NULL,
  client_type                 TEXT NOT NULL
                              CHECK (client_type IN ('public', 'confidential')),
  client_secret_hash          TEXT,
  token_endpoint_auth_method  TEXT NOT NULL DEFAULT 'client_secret_basic'
                              CHECK (token_endpoint_auth_method IN
                                ('none', 'client_secret_basic', 'client_secret_post')),
  allowed_scopes              TEXT NOT NULL DEFAULT '',
  is_first_party              INTEGER NOT NULL DEFAULT 0,
  require_consent             INTEGER NOT NULL DEFAULT 1,
  is_active                   INTEGER NOT NULL DEFAULT 1,
  created_at                  DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
  updated_at                  DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
  deleted_at                  DATETIME
);

-- -----------------------------------------------------------------------------
-- OAuth client redirect URIs (exact match)
-- -----------------------------------------------------------------------------
CREATE TABLE oauth_client_redirect_uris (
  id              TEXT PRIMARY KEY NOT NULL,
  oauth_client_id TEXT NOT NULL,
  redirect_uri    TEXT NOT NULL,
  created_at      DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
  UNIQUE (oauth_client_id, redirect_uri),
  FOREIGN KEY (oauth_client_id) REFERENCES oauth_clients(id) ON DELETE CASCADE
);

-- -----------------------------------------------------------------------------
-- OAuth client post-logout redirect URIs (OIDC RP-Initiated Logout)
-- -----------------------------------------------------------------------------
CREATE TABLE oauth_client_post_logout_redirect_uris (
  id              TEXT PRIMARY KEY NOT NULL,
  oauth_client_id TEXT NOT NULL,
  redirect_uri    TEXT NOT NULL,
  created_at      DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
  UNIQUE (oauth_client_id, redirect_uri),
  FOREIGN KEY (oauth_client_id) REFERENCES oauth_clients(id) ON DELETE CASCADE
);

-- -----------------------------------------------------------------------------
-- OAuth authorization codes (single-use, hashed, PKCE S256 only)
-- -----------------------------------------------------------------------------
CREATE TABLE oauth_authorization_codes (
  id                     TEXT PRIMARY KEY NOT NULL,
  code_hash              TEXT NOT NULL UNIQUE,
  oauth_client_id        TEXT NOT NULL,
  user_id                TEXT NOT NULL,
  session_id             TEXT NOT NULL,
  redirect_uri           TEXT NOT NULL,
  scope                  TEXT NOT NULL DEFAULT '',
  code_challenge         TEXT NOT NULL,
  code_challenge_method  TEXT NOT NULL DEFAULT 'S256'
                         CHECK (code_challenge_method = 'S256'),
  state                  TEXT,
  nonce                  TEXT,
  created_at             DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
  expires_at             DATETIME NOT NULL,
  consumed_at            DATETIME,
  FOREIGN KEY (oauth_client_id) REFERENCES oauth_clients(id) ON DELETE CASCADE,
  FOREIGN KEY (user_id)       REFERENCES users(id)    ON DELETE CASCADE,
  FOREIGN KEY (session_id)    REFERENCES sessions(id) ON DELETE CASCADE
);
CREATE INDEX idx_oauth_authz_codes_client_id  ON oauth_authorization_codes(oauth_client_id);
CREATE INDEX idx_oauth_authz_codes_user_id    ON oauth_authorization_codes(user_id);
CREATE INDEX idx_oauth_authz_codes_expires_at ON oauth_authorization_codes(expires_at);

-- -----------------------------------------------------------------------------
-- OAuth refresh tokens (rotation + family reuse detection)
-- -----------------------------------------------------------------------------
CREATE TABLE oauth_refresh_tokens (
  id                   TEXT PRIMARY KEY NOT NULL,
  token_hash           TEXT NOT NULL UNIQUE,
  family_id            TEXT NOT NULL,
  oauth_client_id      TEXT NOT NULL,
  user_id              TEXT NOT NULL,
  parent_token_id      TEXT,
  replaced_by_token_id TEXT,
  scope                TEXT NOT NULL DEFAULT '',
  created_at           DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
  expires_at           DATETIME NOT NULL,
  rotated_at           DATETIME,
  revoked_at           DATETIME,
  reused_at            DATETIME,
  FOREIGN KEY (oauth_client_id)      REFERENCES oauth_clients(id)         ON DELETE CASCADE,
  FOREIGN KEY (user_id)              REFERENCES users(id)                 ON DELETE CASCADE,
  FOREIGN KEY (parent_token_id)      REFERENCES oauth_refresh_tokens(id)  ON DELETE CASCADE,
  FOREIGN KEY (replaced_by_token_id) REFERENCES oauth_refresh_tokens(id)  ON DELETE SET NULL
);
CREATE INDEX idx_oauth_refresh_tokens_client_user ON oauth_refresh_tokens(oauth_client_id, user_id);
CREATE INDEX idx_oauth_refresh_tokens_family_id   ON oauth_refresh_tokens(family_id);
CREATE INDEX idx_oauth_refresh_tokens_expires_at  ON oauth_refresh_tokens(expires_at);

-- -----------------------------------------------------------------------------
-- OAuth per-(user, client) consents
-- -----------------------------------------------------------------------------
CREATE TABLE oauth_consents (
  id              TEXT PRIMARY KEY NOT NULL,
  user_id         TEXT NOT NULL,
  oauth_client_id TEXT NOT NULL,
  granted_scopes  TEXT NOT NULL DEFAULT '',
  created_at      DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
  revoked_at      DATETIME,
  UNIQUE (user_id, oauth_client_id),
  FOREIGN KEY (user_id)         REFERENCES users(id)         ON DELETE CASCADE,
  FOREIGN KEY (oauth_client_id) REFERENCES oauth_clients(id) ON DELETE CASCADE
);

-- -----------------------------------------------------------------------------
-- OIDC signing keys (rotation lifecycle)
-- -----------------------------------------------------------------------------
CREATE TABLE oauth_signing_keys (
  id                    TEXT PRIMARY KEY NOT NULL,
  kid                   TEXT NOT NULL UNIQUE,
  alg                   TEXT NOT NULL,
  public_jwk            TEXT NOT NULL,
  private_jwk_encrypted TEXT NOT NULL,
  status                TEXT NOT NULL DEFAULT 'pending'
                        CHECK (status IN ('pending', 'active', 'retired')),
  activated_at          DATETIME,
  retired_at            DATETIME,
  created_at            DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now'))
);

-- -----------------------------------------------------------------------------
-- Audit log (append-only)
-- -----------------------------------------------------------------------------
CREATE TABLE audit_logs (
  id              TEXT PRIMARY KEY NOT NULL,
  actor_user_id   TEXT,
  actor_client_id TEXT,
  event_type      TEXT NOT NULL,
  target_type     TEXT,
  target_id       TEXT,
  ip_address      TEXT,
  user_agent      TEXT,
  metadata        TEXT NOT NULL DEFAULT '{}',
  created_at      DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
  FOREIGN KEY (actor_user_id)   REFERENCES users(id)         ON DELETE SET NULL,
  FOREIGN KEY (actor_client_id) REFERENCES oauth_clients(id) ON DELETE SET NULL
);
CREATE INDEX idx_audit_logs_created_at ON audit_logs(created_at);
CREATE INDEX idx_audit_logs_event_type ON audit_logs(event_type);
