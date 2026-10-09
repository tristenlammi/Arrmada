-- 0122_jobs: a record of background work started by a person or an event — a search, a
-- scan, an import, a Run now — so staff can see what is running, what it found and why
-- something failed. Kind and target name the work ('movie.search', 'movie:12'); the job
-- runner keeps one job per (kind, target) at a time. Never holds listening activity:
-- audiobook-server jobs use target 'all'. Times are unix milliseconds, 0 for not yet.
-- Finished rows are pruned after 14 days, keeping at most 5000.
CREATE TABLE IF NOT EXISTS jobs (
    id          INTEGER PRIMARY KEY,
    kind        TEXT    NOT NULL,
    target      TEXT    NOT NULL DEFAULT '',
    trigger     TEXT    NOT NULL DEFAULT '',
    status      TEXT    NOT NULL,  -- queued | running | succeeded | failed | cancelled | panicked | interrupted
    progress    REAL    NOT NULL DEFAULT 0,
    message     TEXT    NOT NULL DEFAULT '',
    error       TEXT    NOT NULL DEFAULT '',
    result      TEXT    NOT NULL DEFAULT '',
    created_at  INTEGER NOT NULL,
    started_at  INTEGER NOT NULL DEFAULT 0,
    finished_at INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_jobs_kind_target ON jobs (kind, target, id DESC);
CREATE INDEX IF NOT EXISTS idx_jobs_status ON jobs (status);
CREATE INDEX IF NOT EXISTS idx_jobs_finished ON jobs (finished_at);
