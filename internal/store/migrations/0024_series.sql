-- 0024_series: the Series (TV) module — a series has many seasons, each with many
-- episodes. Episodes are the unit that gets monitored, searched, and stored.

CREATE TABLE series (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    tmdb_id         INTEGER NOT NULL UNIQUE,
    imdb_id         TEXT NOT NULL DEFAULT '',
    title           TEXT NOT NULL,
    year            INTEGER NOT NULL DEFAULT 0,
    overview        TEXT NOT NULL DEFAULT '',
    poster_url      TEXT NOT NULL DEFAULT '',
    status          TEXT NOT NULL DEFAULT '',   -- Returning Series | Ended | Canceled
    network         TEXT NOT NULL DEFAULT '',
    monitored       INTEGER NOT NULL DEFAULT 1,
    quality_profile TEXT NOT NULL DEFAULT '',
    extra_json      TEXT NOT NULL DEFAULT '',
    added_at        TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE seasons (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    series_id     INTEGER NOT NULL REFERENCES series(id) ON DELETE CASCADE,
    season_number INTEGER NOT NULL,
    name          TEXT NOT NULL DEFAULT '',
    overview      TEXT NOT NULL DEFAULT '',
    poster_url    TEXT NOT NULL DEFAULT '',
    monitored     INTEGER NOT NULL DEFAULT 1,
    UNIQUE(series_id, season_number)
);

CREATE TABLE episodes (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    series_id      INTEGER NOT NULL REFERENCES series(id) ON DELETE CASCADE,
    season_number  INTEGER NOT NULL,
    episode_number INTEGER NOT NULL,
    title          TEXT NOT NULL DEFAULT '',
    overview       TEXT NOT NULL DEFAULT '',
    air_date       TEXT NOT NULL DEFAULT '',
    runtime        INTEGER NOT NULL DEFAULT 0,
    still_url      TEXT NOT NULL DEFAULT '',
    monitored      INTEGER NOT NULL DEFAULT 1,
    has_file       INTEGER NOT NULL DEFAULT 0,
    file_path      TEXT NOT NULL DEFAULT '',
    size_bytes     INTEGER NOT NULL DEFAULT 0,
    UNIQUE(series_id, season_number, episode_number)
);

CREATE INDEX idx_seasons_series ON seasons(series_id);
CREATE INDEX idx_episodes_series ON episodes(series_id);
