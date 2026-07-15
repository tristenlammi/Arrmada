-- 0038_convert_failures: Convert C4 — failure quarantine. Tracks how many times converting a
-- given movie has failed so the nightly auto-sweep can skip a file that keeps breaking (a bad
-- rip, an unsupported stream) instead of re-queuing it every night. A successful convert clears
-- the row; a manual run ignores the blocklist. See CONVERT-BUILD-PLAN.md (C4).

CREATE TABLE IF NOT EXISTS convert_failures (
    movie_id   INTEGER PRIMARY KEY,
    count      INTEGER NOT NULL DEFAULT 0,
    last_error TEXT    NOT NULL DEFAULT '',
    updated_at TEXT    NOT NULL DEFAULT (datetime('now'))
);
