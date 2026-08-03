-- 0022_mail_outbox_retry.sql
-- Outbox auto-retry tracking. A message left at is_outbox=1 after a failed
-- driver send is retried by a background sweeper. These columns let the
-- sweeper back off (next_attempt_at) and give up after a bounded number of
-- attempts (attempts, max_attempts), so a permanently-undeliverable message
-- does not loop forever.

ALTER TABLE mail_messages ADD COLUMN outbox_attempts      INTEGER NOT NULL DEFAULT 0;
ALTER TABLE mail_messages ADD COLUMN outbox_next_attempt  DATETIME;

-- Index the sweeper's lookup: rows still pending delivery whose backoff
-- window has elapsed. The partial WHERE keeps the index small.
CREATE INDEX idx_mail_messages_outbox_retry
  ON mail_messages(is_outbox, outbox_next_attempt)
  WHERE is_outbox = 1;
