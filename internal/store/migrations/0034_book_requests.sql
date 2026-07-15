-- 0034_book_requests: generalize the Requests module to books. Books are keyed by an
-- Open Library work id (ol_key), not a TMDB id, so requests gains ol_key + author. The
-- old table-level UNIQUE(media_type, tmdb_id) can't express "unique per ol_key for books"
-- (every book row has tmdb_id = 0), and SQLite can't drop a table constraint in place, so
-- rebuild the table and replace the constraint with two partial unique indexes.

CREATE TABLE requests_new (
    id                INTEGER PRIMARY KEY AUTOINCREMENT,
    media_type        TEXT NOT NULL,                        -- 'movie' | 'series' | 'book'
    tmdb_id           INTEGER NOT NULL DEFAULT 0,           -- movies/series
    ol_key            TEXT NOT NULL DEFAULT '',             -- books (Open Library work key)
    title             TEXT NOT NULL,
    author            TEXT NOT NULL DEFAULT '',             -- books
    year              INTEGER NOT NULL DEFAULT 0,
    poster_url        TEXT NOT NULL DEFAULT '',
    overview          TEXT NOT NULL DEFAULT '',
    status            TEXT NOT NULL DEFAULT 'pending',       -- pending | approved | declined
    quality_profile   TEXT NOT NULL DEFAULT '',
    requested_by      INTEGER NOT NULL DEFAULT 0,
    requested_by_name TEXT NOT NULL DEFAULT '',
    note              TEXT NOT NULL DEFAULT '',
    created_at        TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at        TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

INSERT INTO requests_new
    (id, media_type, tmdb_id, title, year, poster_url, overview, status,
     quality_profile, requested_by, requested_by_name, note, created_at, updated_at)
SELECT
    id, media_type, tmdb_id, title, year, poster_url, overview, status,
    quality_profile, requested_by, requested_by_name, note, created_at, updated_at
FROM requests;

DROP TABLE requests;
ALTER TABLE requests_new RENAME TO requests;

CREATE INDEX IF NOT EXISTS idx_requests_status ON requests(status, id DESC);
-- One request per movie/series (by tmdb_id) and one per book (by ol_key).
CREATE UNIQUE INDEX IF NOT EXISTS idx_requests_tmdb ON requests(media_type, tmdb_id) WHERE media_type IN ('movie', 'series');
CREATE UNIQUE INDEX IF NOT EXISTS idx_requests_ol ON requests(media_type, ol_key) WHERE media_type = 'book';
