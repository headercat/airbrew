-- 0007_vault_hardening.sql
-- Schema additions for the password-vault hardening pass.
--
-- 1. vault_keys.version: monotonic counter bumped on every envelope rewrite
--    (setup/rotate). Exposed as the optimistic-concurrency guard for master-
--    password rotation so two concurrent rotations cannot silently clobber
--    each other and lock a device out of its vault.
-- 2. idx_vault_attachments_item: ListAttachments / ownership-scoped reads join
--    on item_id; without an index the scan is linear per item.

ALTER TABLE vault_keys ADD COLUMN version INTEGER NOT NULL DEFAULT 1;

CREATE INDEX idx_vault_attachments_item ON vault_attachments(item_id);
