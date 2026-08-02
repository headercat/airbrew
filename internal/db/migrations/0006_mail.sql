-- 0006_mail.sql
-- Mail module: per-user mailboxes, inbound/outbound messages, and the
-- admin-selectable provider (driver) registry. See docs/mail.md.
--
-- One mailbox == one email address owned by a user. Messages store both
-- inbound (received) and outbound (sent) mail; the raw RFC822 is kept in the
-- blob store and referenced by raw_path. Recipient address lists are JSON
-- arrays of {"name","address"} objects.
--
-- mail_providers holds the driver registry; exactly one inbound and one
-- outbound driver may be active at a time, enforced by partial unique indexes.

-- Per-user mailbox (email address)
CREATE TABLE mailboxes (
  id           TEXT PRIMARY KEY NOT NULL,
  user_id      TEXT NOT NULL,
  local_part   TEXT NOT NULL,
  domain       TEXT NOT NULL,
  address      TEXT NOT NULL,          -- lowercased local@domain
  display_name TEXT,
  is_primary   INTEGER NOT NULL DEFAULT 0,
  created_at   DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now')),
  updated_at   DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now')),
  UNIQUE (user_id, address),
  FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
);
CREATE UNIQUE INDEX idx_mailboxes_address ON mailboxes(address);
CREATE INDEX idx_mailboxes_user ON mailboxes(user_id);

-- Messages (inbound + outbound)
CREATE TABLE mail_messages (
  id            TEXT PRIMARY KEY NOT NULL,
  mailbox_id    TEXT NOT NULL,
  user_id       TEXT NOT NULL,
  message_id    TEXT,                  -- RFC822 Message-ID header
  in_reply_to   TEXT,
  subject       TEXT,
  from_addr     TEXT NOT NULL DEFAULT '{}',   -- JSON {"name","address"}
  to_addrs      TEXT NOT NULL DEFAULT '[]',   -- JSON array
  cc_addrs      TEXT NOT NULL DEFAULT '[]',
  bcc_addrs     TEXT NOT NULL DEFAULT '[]',
  reply_to_addrs TEXT NOT NULL DEFAULT '[]',
  direction     TEXT NOT NULL CHECK (direction IN ('inbound','outbound')),
  raw_path      TEXT,                  -- blob path to full RFC822
  body_text     TEXT,
  body_html     TEXT,
  is_read       INTEGER NOT NULL DEFAULT 0,
  is_starred    INTEGER NOT NULL DEFAULT 0,
  is_draft      INTEGER NOT NULL DEFAULT 0,
  is_outbox     INTEGER NOT NULL DEFAULT 0,
  size_bytes    INTEGER NOT NULL DEFAULT 0,
  received_at   DATETIME,
  sent_at       DATETIME,
  created_at    DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now')),
  updated_at    DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now')),
  FOREIGN KEY (mailbox_id) REFERENCES mailboxes(id) ON DELETE CASCADE,
  FOREIGN KEY (user_id)    REFERENCES users(id)     ON DELETE CASCADE
);
CREATE INDEX idx_mail_messages_mailbox ON mail_messages(mailbox_id, created_at DESC);
CREATE INDEX idx_mail_messages_user    ON mail_messages(user_id, created_at DESC);
CREATE INDEX idx_mail_messages_msgid   ON mail_messages(message_id);

-- Admin-selectable driver registry
CREATE TABLE mail_providers (
  id         TEXT PRIMARY KEY NOT NULL,
  direction  TEXT NOT NULL CHECK (direction IN ('inbound','outbound')),
  driver     TEXT NOT NULL,
  config     TEXT NOT NULL DEFAULT '{}',   -- JSON, provider-specific incl. secrets
  is_active  INTEGER NOT NULL DEFAULT 0,
  created_at DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now')),
  updated_at DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now'))
);
-- At most one active provider per direction.
CREATE UNIQUE INDEX idx_mail_providers_active_inbound
  ON mail_providers(direction) WHERE direction = 'inbound'  AND is_active = 1;
CREATE UNIQUE INDEX idx_mail_providers_active_outbound
  ON mail_providers(direction) WHERE direction = 'outbound' AND is_active = 1;
