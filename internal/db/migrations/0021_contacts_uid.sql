-- 0021_contacts_uid.sql
-- Contacts: stable external id (vCard UID) for import dedup.
--
-- A vCard UID lets re-import of an exported file match and update existing
-- contacts instead of creating duplicates. The column is nullable so legacy
-- rows are unaffected; the partial unique index enforces one row per
-- (user, uid) only when a uid is actually set.

ALTER TABLE contacts ADD COLUMN uid TEXT;
CREATE UNIQUE INDEX idx_contacts_user_uid
  ON contacts(user_id, uid)
  WHERE uid IS NOT NULL AND uid != '';
