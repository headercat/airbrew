-- 0017_vault_crypto_version.sql
-- Track whether ciphertext was written before or after AES-GCM AAD labels.
-- Version 1 rows are legacy/no-AAD compatible; version 2 rows must decrypt
-- with the field-specific AAD label and must not fall back to legacy mode.

ALTER TABLE vault_keys ADD COLUMN crypto_version INTEGER NOT NULL DEFAULT 1;
ALTER TABLE vault_folders ADD COLUMN crypto_version INTEGER NOT NULL DEFAULT 1;
ALTER TABLE vault_items ADD COLUMN crypto_version INTEGER NOT NULL DEFAULT 1;
ALTER TABLE vault_item_revisions ADD COLUMN crypto_version INTEGER NOT NULL DEFAULT 1;
ALTER TABLE vault_attachments ADD COLUMN crypto_version INTEGER NOT NULL DEFAULT 1;
