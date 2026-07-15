-- 0009_movie_versions: opt-in multi-version support. The movie's own row remains
-- the DEFAULT version (so single-file movies are unchanged); each EXTRA version
-- track (e.g. a 4K track alongside 1080p, or a Director's Cut) is a row here.

CREATE TABLE movie_versions (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    movie_id        INTEGER NOT NULL,
    label           TEXT NOT NULL,
    quality_profile TEXT NOT NULL DEFAULT '4k-hdr',
    edition         TEXT NOT NULL DEFAULT '',
    monitored       INTEGER NOT NULL DEFAULT 1,
    has_file        INTEGER NOT NULL DEFAULT 0,
    file_path       TEXT NOT NULL DEFAULT '',
    size_bytes      INTEGER NOT NULL DEFAULT 0,
    created_at      TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX idx_movie_versions_movie ON movie_versions(movie_id);
