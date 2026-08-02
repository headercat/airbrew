-- 0010_ai_unique_seq.sql
-- Enforce (conversation_id, seq) uniqueness so two concurrent
-- AppendMessage calls cannot silently produce two rows with the same
-- seq number. The repository was already guarding on a per-conversation
-- MAX(seq) inside a transaction, but SQLite's default transaction
-- isolation did not prevent the rare interleaving under WAL. The unique
-- index lets the second writer detect the race and retry / surface a
-- friendly error.

CREATE UNIQUE INDEX IF NOT EXISTS idx_ai_messages_conv_seq_unique
  ON ai_messages(conversation_id, seq);
