-- 0027_series_events: per-series activity timeline (added / grabbed / imported /
-- renamed / failed), mirroring movie_events. Powers the History panel on the series
-- detail page. Cascade-deletes with the series.

CREATE TABLE IF NOT EXISTS series_events (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    series_id  INTEGER NOT NULL REFERENCES series(id) ON DELETE CASCADE,
    event      TEXT NOT NULL,
    detail     TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_series_events_series ON series_events(series_id, id DESC);
