-- 0012_mail_attachments.sql
-- Per-message attachments. Backs both inbound (parsed from MIME parts during
-- ingest) and outbound (uploaded by the composer, then linked to the sent
-- message). The binary payload lives in the blob store (internal/blob) under
-- the mail-attachments namespace; this table holds the metadata + ownership.
--
-- For outbound composition, a row may exist with message_id NULL ("pending")
-- until the send creates the message row and links it; an unlinked pending row
-- is a draft attachment awaiting send or cleanup.

CREATE TABLE mail_attachments (
  id            TEXT PRIMARY KEY NOT NULL,
  message_id    TEXT,                 -- NULL while a pending outbound upload
  user_id       TEXT NOT NULL,
  blob_path     TEXT NOT NULL,
  filename      TEXT NOT NULL DEFAULT '',
  content_type  TEXT NOT NULL DEFAULT 'application/octet-stream',
  content_id    TEXT,                  -- CID for inline (embedded) images
  disposition   TEXT NOT NULL DEFAULT 'attachment'
                 CHECK (disposition IN ('attachment','inline')),
  size_bytes    INTEGER NOT NULL DEFAULT 0,
  created_at    DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now')),
  FOREIGN KEY (message_id) REFERENCES mail_messages(id) ON DELETE CASCADE,
  FOREIGN KEY (user_id)      REFERENCES users(id)        ON DELETE CASCADE
);
CREATE INDEX idx_mail_attachments_message ON mail_attachments(message_id);
CREATE INDEX idx_mail_attachments_user    ON mail_attachments(user_id, created_at DESC);
