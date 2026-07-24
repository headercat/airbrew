-- 0002_user_roles.sql
-- Adds a role column to users so the bootstrap admin (and future OAuth admin
-- endpoints) can be distinguished from regular workspace members.

ALTER TABLE users ADD COLUMN role TEXT NOT NULL DEFAULT 'user'
  CHECK (role IN ('user', 'admin'));

CREATE INDEX idx_users_role ON users(role);
