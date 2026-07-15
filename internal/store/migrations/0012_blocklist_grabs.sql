-- 0012_blocklist_grabs: release blocklist + grab tracking for stall detection.

CREATE TABLE blocklist (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    movie_id     INTEGER NOT NULL DEFAULT 0,
    norm_title   TEXT NOT NULL,
    title        TEXT NOT NULL,
    indexer      TEXT NOT NULL DEFAULT '',
    download_url TEXT NOT NULL DEFAULT '',
    reason       TEXT NOT NULL DEFAULT '',
    created_at   TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX idx_blocklist_movie ON blocklist(movie_id);

-- Every automatic grab, so a background monitor can fail over stalled downloads.
CREATE TABLE grabs (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    movie_id        INTEGER NOT NULL,
    version_id      INTEGER NOT NULL DEFAULT 0,
    title           TEXT NOT NULL,
    indexer         TEXT NOT NULL DEFAULT '',
    quality_profile TEXT NOT NULL DEFAULT '',
    stall_minutes   INTEGER NOT NULL DEFAULT 0,
    status          TEXT NOT NULL DEFAULT 'grabbed',  -- grabbed | imported | failed
    grabbed_at      TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX idx_grabs_status ON grabs(status);
