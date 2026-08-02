-- 0011_mail_threads.sql
-- Conversation threading and inbound deduplication. See docs/mail.md.
--
-- thread_id groups messages into a conversation. It is computed at ingest/send
-- time from the References + In-Reply-To + Message-ID chain (the oldest id wins;
-- a message with no references starts its own thread keyed by its own id).
--
-- references stores the normalized RFC5322 References list as a JSON array so
-- the client can render threading and the server can recompute groups if the
-- algorithm changes.
--
-- The partial UNIQUE index makes (mailbox_id, message_id) unique within a
-- mailbox so a webhook retry or an IMAP re-fetch cannot double-ingest the same
-- message. message_id is empty only for legacy rows / parse failures, which are
-- exempt from the constraint.

ALTER TABLE mail_messages ADD COLUMN thread_id   TEXT;
ALTER TABLE mail_messages ADD COLUMN refs        TEXT NOT NULL DEFAULT '[]';

CREATE UNIQUE INDEX idx_mail_messages_dedup
  ON mail_messages(mailbox_id, message_id)
  WHERE message_id IS NOT NULL AND message_id != '';

-- Look up a conversation quickly for a user.
CREATE INDEX idx_mail_messages_thread ON mail_messages(user_id, thread_id, created_at DESC);
