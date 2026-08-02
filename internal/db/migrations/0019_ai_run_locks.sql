CREATE TABLE ai_run_locks (
  conversation_id TEXT PRIMARY KEY NOT NULL,
  run_id          TEXT NOT NULL,
  expires_at      TEXT NOT NULL,
  created_at      TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now')),
  FOREIGN KEY (conversation_id) REFERENCES ai_conversations(id) ON DELETE CASCADE
);

CREATE INDEX idx_ai_run_locks_expires ON ai_run_locks(expires_at);
