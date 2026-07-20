-- 0060_convert_skips: remember why a file was skipped.
--
-- Skipping a file is a legitimate outcome — Dolby Vision can't be carried into AV1, a file
-- that's still seeding shouldn't be touched. Skipping it INVISIBLY is not, and that's what
-- happened: a skip recorded nothing, the in-memory job list holds only the last 200 entries
-- and is lost on restart, and the file stayed a candidate forever. So the Overview kept
-- promising space that was never coming, every sweep re-probed the file (waking the array)
-- to skip it again, and the user had no way to discover which files were affected or why.
--
-- Keyed the same way as convert_failures (see migration 0059) so movies and episodes share
-- one shape: "movie:12", "episode:76:2:5".
CREATE TABLE IF NOT EXISTS convert_skips (
    item_key   TEXT PRIMARY KEY,
    kind       TEXT    NOT NULL,            -- machine-readable category, for grouping
    reason     TEXT    NOT NULL DEFAULT '', -- what to show the user
    -- permanent means "this will not change on its own". A Dolby Vision file can't become
    -- convertible to AV1 by waiting; a seeding file can. Only permanent skips are excluded
    -- from the reclaimable-space figure.
    permanent  INTEGER NOT NULL DEFAULT 0,
    updated_at TEXT    NOT NULL DEFAULT (datetime('now'))
);

CREATE INDEX IF NOT EXISTS idx_convert_skips_kind ON convert_skips(kind);
