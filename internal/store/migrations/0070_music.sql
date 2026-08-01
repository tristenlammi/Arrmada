-- 0070_music: the Music module (Lidarr replacement). Keyed by MusicBrainz ids (MBIDs,
-- which are UUID strings) rather than a numeric provider id, the way books are keyed by
-- their Open Library work id.
--
-- The acquisition unit is the ALBUM, not the track: releases ship as an album, and a
-- quality profile for music is a FORMAT preference (FLAC > MP3 320 > …), scored through the
-- shared quality_profiles table's format_scores exactly like books.
--
-- Tracks carry the file state so a partially-complete album is visible per track, which is
-- what tells the searcher an album still needs work.

CREATE TABLE IF NOT EXISTS artists (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    mbid            TEXT NOT NULL UNIQUE,
    name            TEXT NOT NULL,
    sort_name       TEXT NOT NULL DEFAULT '',
    overview        TEXT NOT NULL DEFAULT '',
    image_url       TEXT NOT NULL DEFAULT '',
    genres_json     TEXT NOT NULL DEFAULT '',
    monitored       INTEGER NOT NULL DEFAULT 1,
    quality_profile TEXT NOT NULL DEFAULT '',
    added_at        TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS albums (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    artist_id    INTEGER NOT NULL REFERENCES artists(id) ON DELETE CASCADE,
    mbid         TEXT NOT NULL UNIQUE,          -- MusicBrainz release-group id
    title        TEXT NOT NULL,
    year         INTEGER NOT NULL DEFAULT 0,
    -- Album | EP | Single | Live | Compilation | Soundtrack | Other. Kept as text because
    -- MusicBrainz's primary/secondary type combinations don't reduce to a fixed enum.
    album_type   TEXT NOT NULL DEFAULT 'Album',
    cover_url    TEXT NOT NULL DEFAULT '',
    release_date TEXT NOT NULL DEFAULT '',      -- YYYY-MM-DD when known
    monitored    INTEGER NOT NULL DEFAULT 1,
    added_at     TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS tracks (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    album_id       INTEGER NOT NULL REFERENCES albums(id) ON DELETE CASCADE,
    mbid           TEXT NOT NULL DEFAULT '',    -- MusicBrainz recording id (blank if unknown)
    disc_number    INTEGER NOT NULL DEFAULT 1,
    track_number   INTEGER NOT NULL DEFAULT 0,
    title          TEXT NOT NULL,
    duration_sec   INTEGER NOT NULL DEFAULT 0,
    monitored      INTEGER NOT NULL DEFAULT 1,
    has_file       INTEGER NOT NULL DEFAULT 0,
    file_path      TEXT NOT NULL DEFAULT '',
    format         TEXT NOT NULL DEFAULT '',    -- FLAC, MP3, OPUS…
    bitrate_kbps   INTEGER NOT NULL DEFAULT 0,
    size_bytes     INTEGER NOT NULL DEFAULT 0,
    source_release TEXT NOT NULL DEFAULT '',    -- the release it was imported from
    UNIQUE(album_id, disc_number, track_number)
);

-- Per-artist activity timeline, mirroring movie_events / series_events / book_events.
CREATE TABLE IF NOT EXISTS artist_events (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    artist_id  INTEGER NOT NULL REFERENCES artists(id) ON DELETE CASCADE,
    event      TEXT NOT NULL,
    detail     TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_artists_added ON artists(added_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_albums_artist ON albums(artist_id, year DESC);
CREATE INDEX IF NOT EXISTS idx_tracks_album ON tracks(album_id, disc_number, track_number);
CREATE INDEX IF NOT EXISTS idx_artist_events_artist ON artist_events(artist_id, id DESC);
