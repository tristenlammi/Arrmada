-- Insights (Plex watch monitoring). The poller records each play session, buffer spells, and a
-- bandwidth time-series into these tables. Timestamps are epoch seconds (UTC).

CREATE TABLE IF NOT EXISTS plex_users (
    id           TEXT PRIMARY KEY,          -- Plex account id
    username     TEXT NOT NULL DEFAULT '',
    thumb        TEXT NOT NULL DEFAULT '',
    last_seen_at INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS stream_sessions (
    id                INTEGER PRIMARY KEY AUTOINCREMENT,
    session_key       TEXT    NOT NULL DEFAULT '',
    user_id           TEXT    NOT NULL DEFAULT '',
    user_name         TEXT    NOT NULL DEFAULT '',
    rating_key        TEXT    NOT NULL DEFAULT '',
    media_type        TEXT    NOT NULL DEFAULT '',
    title             TEXT    NOT NULL DEFAULT '',
    grandparent_title TEXT    NOT NULL DEFAULT '',
    parent_title      TEXT    NOT NULL DEFAULT '',
    media_index       INTEGER NOT NULL DEFAULT 0,
    parent_index      INTEGER NOT NULL DEFAULT 0,
    year              INTEGER NOT NULL DEFAULT 0,
    thumb             TEXT    NOT NULL DEFAULT '',
    player            TEXT    NOT NULL DEFAULT '',
    platform          TEXT    NOT NULL DEFAULT '',
    product           TEXT    NOT NULL DEFAULT '',
    ip_address        TEXT    NOT NULL DEFAULT '',
    location          TEXT    NOT NULL DEFAULT '',
    decision          TEXT    NOT NULL DEFAULT '',
    started_at        INTEGER NOT NULL DEFAULT 0, -- epoch seconds
    stopped_at        INTEGER NOT NULL DEFAULT 0,
    paused_ms         INTEGER NOT NULL DEFAULT 0,
    view_offset_ms    INTEGER NOT NULL DEFAULT 0,
    duration_ms       INTEGER NOT NULL DEFAULT 0,
    video_src         TEXT    NOT NULL DEFAULT '',
    video_stream      TEXT    NOT NULL DEFAULT '',
    audio_src         TEXT    NOT NULL DEFAULT '',
    audio_stream      TEXT    NOT NULL DEFAULT '',
    container_src     TEXT    NOT NULL DEFAULT '',
    container_stream  TEXT    NOT NULL DEFAULT '',
    hw_transcode      INTEGER NOT NULL DEFAULT 0,
    buffer_count      INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_stream_sessions_started ON stream_sessions(started_at DESC);
CREATE INDEX IF NOT EXISTS idx_stream_sessions_user ON stream_sessions(user_id);
CREATE INDEX IF NOT EXISTS idx_stream_sessions_rating ON stream_sessions(rating_key);
CREATE INDEX IF NOT EXISTS idx_stream_sessions_type ON stream_sessions(media_type);

CREATE TABLE IF NOT EXISTS buffer_events (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id     INTEGER NOT NULL,
    at             INTEGER NOT NULL DEFAULT 0, -- epoch seconds
    view_offset_ms INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_buffer_events_session ON buffer_events(session_id);
CREATE INDEX IF NOT EXISTS idx_buffer_events_at ON buffer_events(at DESC);

CREATE TABLE IF NOT EXISTS bandwidth_samples (
    at         INTEGER NOT NULL DEFAULT 0, -- epoch seconds
    total_kbps INTEGER NOT NULL DEFAULT 0,
    lan_kbps   INTEGER NOT NULL DEFAULT 0,
    wan_kbps   INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_bandwidth_samples_at ON bandwidth_samples(at DESC);
