-- 0121_scheduled_tasks: each recurring task's last run, kept across restarts so the
-- System → Status Tasks table can still say "last ran 3 h ago, failed: …" after a deploy.
-- One row per task name, upserted after runs (sub-minute tasks at most every 5 minutes or
-- when the outcome flips). Times are unix milliseconds, 0 for never.
CREATE TABLE IF NOT EXISTS scheduled_tasks (
    name                 TEXT    PRIMARY KEY,
    every_seconds        INTEGER NOT NULL DEFAULT 0,
    last_started_at      INTEGER NOT NULL DEFAULT 0,
    last_finished_at     INTEGER NOT NULL DEFAULT 0,
    last_duration_ms     INTEGER NOT NULL DEFAULT 0,
    last_status          TEXT    NOT NULL DEFAULT '',  -- '' | ok | failed | panicked
    last_error           TEXT    NOT NULL DEFAULT '',
    last_error_at        INTEGER NOT NULL DEFAULT 0,
    runs                 INTEGER NOT NULL DEFAULT 0,
    failures             INTEGER NOT NULL DEFAULT 0,
    consecutive_failures INTEGER NOT NULL DEFAULT 0
);
