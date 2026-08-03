-- 0024_user_email_lowercase.sql
-- Normalise existing email rows to lowercase so the case-insensitive lookup
-- (COLLATE NOCASE) and the lowercase-on-register write path agree on a single
-- canonical form. Without this backfill, a pre-existing "Alice@x.com" row and
-- a new "alice@x.com" registration could be treated as distinct by code paths
-- that still compare case-sensitively. SQLite's LOWER() is ASCII-only, which
-- is correct for the local-part of an email and conservative for the domain.

UPDATE users SET email = LOWER(email), updated_at = updated_at WHERE email <> LOWER(email);
