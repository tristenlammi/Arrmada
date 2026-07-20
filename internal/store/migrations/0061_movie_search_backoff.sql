-- 0061_movie_search_backoff: back off searching a movie that keeps finding nothing.
--
-- The same problem 0055 fixed for series, still live for movies. The missing-sweep
-- searched EVERY monitored movie with no file, every cycle, forever. A film no indexer
-- carries — an unreleased 2026 title, or one where every result is for a different film
-- of the same name — cost a full multi-indexer search every five minutes indefinitely.
--
-- Observed in the wild: seven movies re-searched at 14:20, 14:25, 14:30, 14:35, 14:40,
-- 14:45 and 14:50, several returning results that were never grabbable.
--
-- search_misses counts consecutive sweeps that grabbed nothing; last_search_at is when we
-- last looked. The sweep skips a movie until an exponential backoff elapses (30m, 1h, 2h …
-- capped at 12h) and resets to 0 the moment a grab succeeds.
--
-- Scoped to the SWEEP only, exactly as for series: a user-triggered "Search indexers"
-- ignores the backoff entirely, and RSS sync is unaffected, so a newly-released film is
-- still picked up promptly.
ALTER TABLE movies ADD COLUMN last_search_at TEXT NOT NULL DEFAULT '';
ALTER TABLE movies ADD COLUMN search_misses INTEGER NOT NULL DEFAULT 0;
