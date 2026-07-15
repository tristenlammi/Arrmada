-- 0028_requests: the Requests module (Overseerr/Jellyseerr replacement). A request is
-- a user asking for a movie or series; on approval it's added to the Movies/Series
-- module (monitored, with a profile) which triggers the normal acquisition. Availability
-- ("do we have it yet") is computed at read time against the library, not stored here.

CREATE TABLE IF NOT EXISTS requests (
    id                INTEGER PRIMARY KEY AUTOINCREMENT,
    media_type        TEXT NOT NULL,                       -- 'movie' | 'series'
    tmdb_id           INTEGER NOT NULL,
    title             TEXT NOT NULL,
    year              INTEGER NOT NULL DEFAULT 0,
    poster_url        TEXT NOT NULL DEFAULT '',
    overview          TEXT NOT NULL DEFAULT '',
    status            TEXT NOT NULL DEFAULT 'pending',      -- pending | approved | declined
    quality_profile   TEXT NOT NULL DEFAULT '',
    requested_by      INTEGER NOT NULL DEFAULT 0,
    requested_by_name TEXT NOT NULL DEFAULT '',
    note              TEXT NOT NULL DEFAULT '',
    created_at        TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at        TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(media_type, tmdb_id)
);

CREATE INDEX IF NOT EXISTS idx_requests_status ON requests(status, id DESC);
