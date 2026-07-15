-- 0006_movies: the Movies library.

CREATE TABLE movies (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    tmdb_id         INTEGER NOT NULL UNIQUE,
    imdb_id         TEXT NOT NULL DEFAULT '',
    title           TEXT NOT NULL,
    year            INTEGER NOT NULL DEFAULT 0,
    overview        TEXT NOT NULL DEFAULT '',
    poster_url      TEXT NOT NULL DEFAULT '',
    runtime         INTEGER NOT NULL DEFAULT 0,
    status          TEXT NOT NULL DEFAULT '',
    monitored       INTEGER NOT NULL DEFAULT 1,
    quality_profile TEXT NOT NULL DEFAULT '4k-hdr',
    has_file        INTEGER NOT NULL DEFAULT 0,
    movie_file_path TEXT NOT NULL DEFAULT '',
    added_at        TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX idx_movies_monitored ON movies(monitored);
