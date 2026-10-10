-- 0178_watched_by_indexes: the Movie and Series pages' "Watched by" line looks a title's
-- plays up by Plex rating key, then by title (and year, for a film) — for plays recorded
-- before Plex re-added the item under a new key, and for imported Tautulli history. The
-- rating key is already indexed; these cover the title lookups, which otherwise scan the
-- whole play history on every detail page.
CREATE INDEX IF NOT EXISTS idx_stream_sessions_title
    ON stream_sessions(media_type, title COLLATE NOCASE, year);
CREATE INDEX IF NOT EXISTS idx_stream_sessions_show
    ON stream_sessions(media_type, grandparent_title COLLATE NOCASE);
