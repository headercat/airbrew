-- 0004_admin_phase1.sql
-- Supports soft-delete recovery and password-change tracking for the admin
-- console's Phase 1 user management surface.

-- Tracks when a user was soft-deleted (status='deleted'). NULL when active.
-- Used to purge records after a 30-day recovery window.
ALTER TABLE users ADD COLUMN deleted_at DATETIME;

-- Records when the password was last changed. Seeded from updated_at on
-- existing rows via the application layer on first password change.
ALTER TABLE password_credentials ADD COLUMN password_changed_at DATETIME;
