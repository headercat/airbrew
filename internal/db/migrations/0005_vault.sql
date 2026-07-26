-- 0005_vault.sql
-- Zero-knowledge password vault. The server stores only ciphertext and key
-- envelopes; it can never decrypt vault contents. See docs/passwords.md.
--
-- Key hierarchy (all crypto runs client-side):
--   master password -> Argon2id -> master key (client only)
--   master key wraps a random 32 B vault key (vault_keys envelope, synced)
--   vault key AES-256-GCM encrypts every item/folder/attachment field
--
-- A per-user monotonic counter (vault_sync_state) drives multi-device delta
-- sync. Every insert/update/soft-delete bumps it and stamps the new value onto
-- the changed row's `revision`; clients pull rows with revision > last seen.

-- Per-user key envelope (1:1 with users)
CREATE TABLE vault_keys (
  user_id                TEXT PRIMARY KEY NOT NULL,
  kdf_algorithm          TEXT NOT NULL DEFAULT 'argon2id',
  kdf_salt               TEXT NOT NULL,
  kdf_memory_kib         INTEGER NOT NULL DEFAULT 65536,
  kdf_iterations         INTEGER NOT NULL DEFAULT 3,
  kdf_parallelism        INTEGER NOT NULL DEFAULT 2,
  protected_vault_key    TEXT NOT NULL,
  protected_vault_nonce  TEXT NOT NULL,
  created_at             DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now')),
  updated_at             DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now')),
  FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
);

-- Per-user monotonic sync cursor
CREATE TABLE vault_sync_state (
  user_id     TEXT PRIMARY KEY NOT NULL,
  current_rev INTEGER NOT NULL DEFAULT 0,
  FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
);

-- Folders (encrypted name)
CREATE TABLE vault_folders (
  id          TEXT PRIMARY KEY NOT NULL,
  user_id     TEXT NOT NULL,
  name_cipher TEXT NOT NULL,
  name_nonce  TEXT NOT NULL,
  revision    INTEGER NOT NULL,
  created_at  DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now')),
  updated_at  DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now')),
  deleted_at  DATETIME,
  FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
);
CREATE INDEX idx_vault_folders_sync ON vault_folders(user_id, revision);

-- Items: login / secure_note / card / identity
CREATE TABLE vault_items (
  id           TEXT PRIMARY KEY NOT NULL,
  user_id      TEXT NOT NULL,
  type         TEXT NOT NULL CHECK (type IN ('login','secure_note','card','identity')),
  folder_id    TEXT,
  name_cipher  TEXT NOT NULL,
  name_nonce   TEXT NOT NULL,
  data_cipher  TEXT NOT NULL,
  data_nonce   TEXT NOT NULL,
  notes_cipher TEXT,
  notes_nonce  TEXT,
  favorite     INTEGER NOT NULL DEFAULT 0,
  reprompt     INTEGER NOT NULL DEFAULT 0,
  revision     INTEGER NOT NULL,
  created_at   DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now')),
  updated_at   DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now')),
  deleted_at   DATETIME,
  FOREIGN KEY (user_id)   REFERENCES users(id)         ON DELETE CASCADE,
  FOREIGN KEY (folder_id) REFERENCES vault_folders(id) ON DELETE SET NULL
);
CREATE INDEX idx_vault_items_sync ON vault_items(user_id, revision);

-- Per-item encrypted history snapshots
CREATE TABLE vault_item_revisions (
  id          TEXT PRIMARY KEY NOT NULL,
  item_id     TEXT NOT NULL,
  name_cipher TEXT NOT NULL,
  name_nonce  TEXT NOT NULL,
  data_cipher TEXT NOT NULL,
  data_nonce  TEXT NOT NULL,
  revision    INTEGER NOT NULL,
  created_at  DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now')),
  FOREIGN KEY (item_id) REFERENCES vault_items(id) ON DELETE CASCADE
);
CREATE INDEX idx_vault_item_revisions_item ON vault_item_revisions(item_id, created_at DESC);

-- Encrypted attachments (binary stored via internal/blob)
CREATE TABLE vault_attachments (
  id             TEXT PRIMARY KEY NOT NULL,
  item_id        TEXT NOT NULL,
  blob_path      TEXT NOT NULL,
  size_bytes     INTEGER NOT NULL,
  file_key_cipher TEXT NOT NULL,
  file_key_nonce  TEXT NOT NULL,
  name_cipher     TEXT NOT NULL,
  name_nonce      TEXT NOT NULL,
  created_at     DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now')),
  FOREIGN KEY (item_id) REFERENCES vault_items(id) ON DELETE CASCADE
);
