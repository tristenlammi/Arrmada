-- 0086_convert_rebuild: Convert works through the library on its own instead of a queue.

-- The title's original language (TMDB), so "keep the original-language audio" can be
-- decided from the index without a lookup per file.
ALTER TABLE convert_library ADD COLUMN orig_lang TEXT NOT NULL DEFAULT '';
-- Which analysis schema info_json was written with. Rows from an older one are re-probed
-- by the next index pass even though the file itself hasn't changed.
ALTER TABLE convert_library ADD COLUMN info_ver INTEGER NOT NULL DEFAULT 0;

-- Files someone asked for by hand ("Convert", a whole show). They go before the automatic
-- picks, run outside the encode hours, and survive a restart — the old in-memory queue
-- forgot everything on every update.
CREATE TABLE IF NOT EXISTS convert_requests (
    item_key     TEXT PRIMARY KEY,
    title        TEXT    NOT NULL DEFAULT '',
    requested_at INTEGER NOT NULL
);

-- A temporary skip (still seeding, cancelled by you) is retried after this time rather
-- than on the next pick. Unix seconds; 0 = no wait.
ALTER TABLE convert_skips ADD COLUMN retry_after INTEGER NOT NULL DEFAULT 0;

-- The outcome of the per-file HEVC-vs-AV1 test, so a retried file doesn't test again.
-- Keyed by path + size: a replaced file gets tested afresh.
CREATE TABLE IF NOT EXISTS convert_choices (
    path       TEXT PRIMARY KEY,
    size_bytes INTEGER NOT NULL,
    codec      TEXT    NOT NULL,
    detail     TEXT    NOT NULL DEFAULT '',
    decided_at INTEGER NOT NULL
);
