-- 0018_password_history.sql
-- Stores prior password hashes so the workspace password policy can prevent
-- reuse of the last N passwords.

CREATE TABLE password_history (
  id            TEXT PRIMARY KEY NOT NULL,
  user_id       TEXT NOT NULL,
  password_hash TEXT NOT NULL,
  created_at    DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
  FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
);

CREATE INDEX idx_password_history_user_created
  ON password_history(user_id, created_at DESC);
