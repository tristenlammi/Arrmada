-- 0007_movie_details: richer metadata, minimum availability, and per-movie history.

ALTER TABLE movies ADD COLUMN extra_json TEXT NOT NULL DEFAULT '';
ALTER TABLE movies ADD COLUMN min_availability TEXT NOT NULL DEFAULT 'released';

-- Per-movie activity timeline (grabbed / imported / upgraded / deleted / renamed / refreshed).
CREATE TABLE movie_events (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    movie_id   INTEGER NOT NULL,
    event      TEXT NOT NULL,
    detail     TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX idx_movie_events_movie ON movie_events(movie_id, id);
