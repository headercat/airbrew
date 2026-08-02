-- 0008_drive.sql
-- Drive module: per-user file storage with a folder tree and public sharing
-- links. See docs/drive.md.
--
-- drive_nodes holds both files and folders in one self-referential table so a
-- file system-like tree (root = parent_id NULL) with move/rename is easy. File
-- bytes live in the blob store (namespace "drive") and are referenced by
-- blob_path; the content hash (sha256) is recorded for integrity checks and
-- future deduplication. Soft-deleted rows (deleted_at set) appear in the trash
-- until purged.
--
-- drive_shares holds public share links (one per row). Access by token is
-- public (no session); a share may optionally require a password and/or expire.

-- Files and folders (unified tree)
CREATE TABLE drive_nodes (
  id           TEXT PRIMARY KEY NOT NULL,
  user_id      TEXT NOT NULL,
  parent_id    TEXT,                       -- NULL = root; self-ref to drive_nodes
  kind         TEXT NOT NULL CHECK (kind IN ('file','folder')),
  name         TEXT NOT NULL,              -- display name (not unique; dupes allowed)
  blob_path    TEXT,                       -- namespace/name in blob store (NULL for folders)
  content_type TEXT,                       -- best-effort MIME (NULL for folders)
  size_bytes   INTEGER NOT NULL DEFAULT 0, -- file size; 0 for folders
  sha256       TEXT,                       -- content hash (NULL for folders)
  is_starred   INTEGER NOT NULL DEFAULT 0,
  deleted_at   DATETIME,                   -- non-NULL => in trash
  created_at   DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now')),
  updated_at   DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now')),
  FOREIGN KEY (user_id)   REFERENCES users(id)      ON DELETE CASCADE,
  FOREIGN KEY (parent_id) REFERENCES drive_nodes(id) ON DELETE CASCADE
);
CREATE INDEX idx_drive_nodes_user_parent ON drive_nodes(user_id, parent_id, deleted_at);
CREATE INDEX idx_drive_nodes_user_starred ON drive_nodes(user_id, is_starred, deleted_at);
CREATE INDEX idx_drive_nodes_user_trash  ON drive_nodes(user_id, deleted_at);
CREATE INDEX idx_drive_nodes_blob_path   ON drive_nodes(blob_path);

-- Public share links
CREATE TABLE drive_shares (
  id           TEXT PRIMARY KEY NOT NULL,
  node_id      TEXT NOT NULL,
  user_id      TEXT NOT NULL,              -- owner (for listing/revoke)
  token        TEXT NOT NULL,              -- URL-safe random share token
  password_hash TEXT,                      -- argon2id PHC string; NULL = no password
  expires_at   DATETIME,                   -- NULL = never expires
  downloads    INTEGER NOT NULL DEFAULT 0,
  is_active    INTEGER NOT NULL DEFAULT 1,
  created_at   DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now')),
  updated_at   DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now')),
  UNIQUE (token),
  FOREIGN KEY (node_id) REFERENCES drive_nodes(id) ON DELETE CASCADE,
  FOREIGN KEY (user_id) REFERENCES users(id)       ON DELETE CASCADE
);
CREATE INDEX idx_drive_shares_node ON drive_shares(node_id);
CREATE INDEX idx_drive_shares_user ON drive_shares(user_id);
