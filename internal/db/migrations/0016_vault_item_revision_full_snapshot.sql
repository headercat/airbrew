-- 0016_vault_item_revision_full_snapshot.sql
-- Store full vault item snapshots so history restore matches user
-- expectations instead of restoring only name/data.

ALTER TABLE vault_item_revisions ADD COLUMN type TEXT;
ALTER TABLE vault_item_revisions ADD COLUMN folder_id TEXT;
ALTER TABLE vault_item_revisions ADD COLUMN notes_cipher TEXT;
ALTER TABLE vault_item_revisions ADD COLUMN notes_nonce TEXT;
ALTER TABLE vault_item_revisions ADD COLUMN favorite INTEGER;
ALTER TABLE vault_item_revisions ADD COLUMN reprompt INTEGER;
