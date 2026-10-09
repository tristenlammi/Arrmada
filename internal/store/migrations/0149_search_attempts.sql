-- 0149_search_attempts: what each title search found and why nothing was taken.
--
-- A search's result went only to the log: the owner couldn't tell whether nothing was
-- found, everything was for a different film, everything was blocklisted, or everything was
-- over the bitrate ceiling. Every movie, series, book and album search now leaves one row
-- here, the newest 20 per title kept (the insert prunes in the same transaction).
--
--   scope         '' (the whole title), 'S03', 'S03E04', 'ebook', 'audiobook', 'v<id>',
--                 'album', 'upgrade'
--   triggered_by  sweep | rss | manual | request | add | upgrade | stall | replace | other
--                 ("trigger" is an SQL keyword, hence the name)
--   outcome       grabbed | nothing_found | none_suitable | indexers_failed |
--                 skipped_in_flight | error
--   reason        the finer search reason code the Search button already reports
--                 (no-releases, none-for-this-title, indexers-paused, ...)
--   reasons_json  {reject code: count} over the distinct releases seen — wrong_title,
--                 blocklisted, pending, out_of_scope and the quality profile's codes
--   top_reason    the most common of those; example is one release it applied to
--   indexer_errors {indexer: redacted error}, failed and paused indexers alike
CREATE TABLE search_attempts (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    media_type      TEXT    NOT NULL,
    media_id        INTEGER NOT NULL,
    scope           TEXT    NOT NULL DEFAULT '',
    triggered_by    TEXT    NOT NULL DEFAULT 'other',
    started_at      INTEGER NOT NULL,
    duration_ms     INTEGER NOT NULL DEFAULT 0,
    returned        INTEGER NOT NULL DEFAULT 0,
    wrong_title     INTEGER NOT NULL DEFAULT 0,
    blocklisted     INTEGER NOT NULL DEFAULT 0,
    pending         INTEGER NOT NULL DEFAULT 0,
    out_of_scope    INTEGER NOT NULL DEFAULT 0,
    rejected        INTEGER NOT NULL DEFAULT 0,
    eligible        INTEGER NOT NULL DEFAULT 0,
    grabbed         INTEGER NOT NULL DEFAULT 0,
    reasons_json    TEXT    NOT NULL DEFAULT '{}',
    top_reason      TEXT    NOT NULL DEFAULT '',
    example         TEXT    NOT NULL DEFAULT '',
    grabbed_titles  TEXT    NOT NULL DEFAULT '[]',
    indexer_errors  TEXT    NOT NULL DEFAULT '{}',
    outcome         TEXT    NOT NULL,
    reason          TEXT    NOT NULL DEFAULT '',
    error           TEXT    NOT NULL DEFAULT ''
);

CREATE INDEX idx_search_attempts_item ON search_attempts(media_type, media_id, id DESC);
