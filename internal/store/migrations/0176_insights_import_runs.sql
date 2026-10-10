-- 0176_insights_import_runs: each Tautulli import is a run with a record, and every play it
-- brings in carries the run's id.
--
-- Imports used to run blind: the counts and any error went to the server log only, so the
-- owner couldn't tell whether an import finished, what it brought in, or undo a bad one.
--
--   status        running | done | failed | timeout | interrupted (the app stopped mid-run;
--                 marked at the next start)
--   total         Tautulli's own row count, processed how many were read so far — the
--                 progress bar; the counts are ImportCounts (insights/import.go)
--   cutoff_at     the "only plays before" date used (unix seconds, 0 = none), so Retry
--                 repeats the same import
--   removed_*     when "Remove this import" deleted the run's plays, and how many
--
-- Plays imported before this migration keep import_run_id 0: they belong to no run, and
-- the double-count repair (PLEX-02) is how they're cleaned up.
CREATE TABLE insights_import_runs (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    job_id       INTEGER NOT NULL DEFAULT 0,
    source       TEXT    NOT NULL DEFAULT 'tautulli',
    started_at   INTEGER NOT NULL,
    finished_at  INTEGER NOT NULL DEFAULT 0,
    status       TEXT    NOT NULL DEFAULT 'running',
    total        INTEGER NOT NULL DEFAULT 0,
    processed    INTEGER NOT NULL DEFAULT 0,
    imported     INTEGER NOT NULL DEFAULT 0,
    duplicates   INTEGER NOT NULL DEFAULT 0,
    overlaps     INTEGER NOT NULL DEFAULT 0,
    invalid      INTEGER NOT NULL DEFAULT 0,
    after_cutoff INTEGER NOT NULL DEFAULT 0,
    failed       INTEGER NOT NULL DEFAULT 0,
    error        TEXT    NOT NULL DEFAULT '',
    cutoff_at    INTEGER NOT NULL DEFAULT 0,
    removed_at   INTEGER NOT NULL DEFAULT 0,
    removed_rows INTEGER NOT NULL DEFAULT 0
);

ALTER TABLE stream_sessions ADD COLUMN import_run_id INTEGER NOT NULL DEFAULT 0;
-- A plain index, not a partial one: lookups are "import_run_id = ?", which SQLite can't
-- prove matches a WHERE import_run_id > 0 index.
CREATE INDEX IF NOT EXISTS idx_stream_sessions_import_run ON stream_sessions(import_run_id);
