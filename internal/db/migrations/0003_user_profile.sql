-- 0003_user_profile.sql
-- Extends users with extended profile fields used by the account settings page.

ALTER TABLE users ADD COLUMN description TEXT;
ALTER TABLE users ADD COLUMN birthday DATETIME;
ALTER TABLE users ADD COLUMN phone_number TEXT;
ALTER TABLE users ADD COLUMN custom_fields TEXT NOT NULL DEFAULT '{}';
