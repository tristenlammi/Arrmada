-- 0055_series_search_backoff: back off searching a series that keeps finding nothing.
--
-- The missing-sweep ran every 15 minutes and searched EVERY monitored series with a
-- gap, forever, with no attempt tracking anywhere in the codebase. An episode no
-- indexer carries (a bad TMDB row, a special, a region-locked episode) therefore cost a
-- full multi-indexer search four times an hour indefinitely — the main driver of HTTP
-- 429s once many series are monitored.
--
-- search_misses counts consecutive sweeps that found nothing to grab; last_search_at is
-- when we last looked. The sweep skips a series until an exponential backoff elapses
-- (30m, 1h, 2h … capped at 12h) and resets to 0 the moment a grab succeeds.
--
-- Deliberately scoped to the SWEEP only: a user-triggered "Search indexers" ignores the
-- backoff, and RSS sync (which catches newly-aired episodes promptly) is unaffected.
ALTER TABLE series ADD COLUMN last_search_at TEXT NOT NULL DEFAULT '';
ALTER TABLE series ADD COLUMN search_misses INTEGER NOT NULL DEFAULT 0;
