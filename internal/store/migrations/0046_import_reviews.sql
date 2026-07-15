-- A finished download that was grabbed for one title but whose content doesn't
-- match it (e.g. "Below Deck" grabbed a "Below Deck Mediterranean" pack) is held
-- here for admin review instead of being silently skipped or mis-imported.
CREATE TABLE import_reviews (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    hash           TEXT    NOT NULL DEFAULT '',   -- download hash (dedup key)
    name           TEXT    NOT NULL,              -- the release/download name
    content_path   TEXT    NOT NULL DEFAULT '',
    media_type     TEXT    NOT NULL DEFAULT '',   -- series | movie
    expected_id    INTEGER NOT NULL DEFAULT 0,    -- the series/movie id it was grabbed for
    expected_title TEXT    NOT NULL DEFAULT '',
    parsed_title   TEXT    NOT NULL DEFAULT '',    -- what the content actually parses to
    reason         TEXT    NOT NULL DEFAULT '',
    size_bytes     INTEGER NOT NULL DEFAULT 0,
    indexer        TEXT    NOT NULL DEFAULT '',
    download_url   TEXT    NOT NULL DEFAULT '',
    status         TEXT    NOT NULL DEFAULT 'pending', -- pending | resolved
    created_at     TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX idx_import_reviews_status ON import_reviews(status);
