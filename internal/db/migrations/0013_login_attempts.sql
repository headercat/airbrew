-- 0013_login_attempts.sql
-- Records every login attempt (success or failure) so the admin security
-- center can show login history and detect brute-force patterns. Successful
-- and failed attempts are both retained; a janitor may prune old rows.

CREATE TABLE login_attempts (
  id          TEXT PRIMARY KEY NOT NULL,
  user_id     TEXT,
  email       TEXT NOT NULL,
  success     INTEGER NOT NULL,
  ip_address  TEXT,
  user_agent  TEXT,
  failure     TEXT,
  created_at  DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
  FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE SET NULL
);
CREATE INDEX idx_login_attempts_user    ON login_attempts(user_id);
CREATE INDEX idx_login_attempts_email   ON login_attempts(email);
CREATE INDEX idx_login_attempts_created ON login_attempts(created_at);
CREATE INDEX idx_login_attempts_success ON login_attempts(success);
