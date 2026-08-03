-- 0023_workflow_last_fired.sql
-- Durable schedule dedup. The trigger scheduler previously kept its "already
-- fired this minute" state in an in-memory map, so a process restart within
-- the matching minute re-fired every cron schedule (duplicate runs). This
-- column persists the last dispatch timestamp; the scheduler skips a schedule
-- whose last_fired_at falls in the same minute as the current tick.

ALTER TABLE workflows ADD COLUMN last_fired_at DATETIME;
