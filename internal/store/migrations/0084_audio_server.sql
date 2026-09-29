-- 0084_audio_server: the audiobook server for listening apps (Lissen and other
-- Audiobookshelf clients) — listening progress with a guarded sync, durable play
-- sessions, a listening log that records when and how long but never what, bookmarks,
-- app passwords, device tokens, and a cache of probed audio file details.

-- Where each user is in each audiobook. updated_at is when the listening this position
-- reflects happened (server time for live reports, the phone's time for offline ones),
-- so an older report can never replace a newer place. pending_* holds a large jump
-- backwards until the same session proves it by carrying on listening from there.
CREATE TABLE listen_progress (
    user_id          INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    item_key         TEXT    NOT NULL,
    position         REAL    NOT NULL DEFAULT 0,
    duration         REAL    NOT NULL DEFAULT 0,
    finished         INTEGER NOT NULL DEFAULT 0,
    finished_at      INTEGER NOT NULL DEFAULT 0,
    updated_at       INTEGER NOT NULL,
    device           TEXT    NOT NULL DEFAULT '',
    session_id       TEXT    NOT NULL DEFAULT '',
    pending_position REAL,
    pending_session  TEXT    NOT NULL DEFAULT '',
    pending_listened REAL    NOT NULL DEFAULT 0,
    pending_at       INTEGER NOT NULL DEFAULT 0,
    hidden           INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (user_id, item_key)
);

-- Earlier saved places, so a user can put their place back if anything goes wrong.
CREATE TABLE listen_history (
    id       INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id  INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    item_key TEXT    NOT NULL,
    position REAL    NOT NULL,
    at       INTEGER NOT NULL,
    device   TEXT    NOT NULL DEFAULT '',
    reason   TEXT    NOT NULL DEFAULT ''
);
CREATE INDEX idx_listen_history ON listen_history(user_id, item_key, at);

-- Play sessions, kept in the database so a restart never makes the server forget one
-- (Audiobookshelf's "session not found" was the root of lost places). Pruned after a
-- month of inactivity.
CREATE TABLE listen_sessions (
    id         TEXT    PRIMARY KEY,
    user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    item_key   TEXT    NOT NULL,
    device_id  TEXT    NOT NULL DEFAULT '',
    device     TEXT    NOT NULL DEFAULT '',
    client     TEXT    NOT NULL DEFAULT '',
    offline    INTEGER NOT NULL DEFAULT 0,
    started_at INTEGER NOT NULL,
    last_at    INTEGER NOT NULL,
    start_pos  REAL    NOT NULL DEFAULT 0,
    cur_pos    REAL    NOT NULL DEFAULT 0,
    listened   REAL    NOT NULL DEFAULT 0,
    closed     INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX idx_listen_sessions_user ON listen_sessions(user_id, last_at);

-- What the admin's listening overview reads: who, when, how long, which device. It has
-- no book column on purpose — the overview cannot show what anyone listened to.
CREATE TABLE listen_log (
    session_id TEXT    PRIMARY KEY,
    user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    device     TEXT    NOT NULL DEFAULT '',
    client     TEXT    NOT NULL DEFAULT '',
    started_at INTEGER NOT NULL,
    ended_at   INTEGER NOT NULL,
    seconds    REAL    NOT NULL DEFAULT 0
);
CREATE INDEX idx_listen_log_user ON listen_log(user_id, started_at);
CREATE INDEX idx_listen_log_started ON listen_log(started_at);

CREATE TABLE listen_bookmarks (
    user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    item_key   TEXT    NOT NULL,
    time       REAL    NOT NULL,
    title      TEXT    NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL,
    PRIMARY KEY (user_id, item_key, time)
);

-- Passwords made just for a listening app, revocable one by one. Plex-login users have
-- no password to type, and nobody's main password goes into a third-party app.
CREATE TABLE audio_app_passwords (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id      INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name         TEXT    NOT NULL,
    hash         TEXT    NOT NULL,
    created_at   INTEGER NOT NULL,
    last_used_at INTEGER NOT NULL DEFAULT 0
);

-- Tokens handed to listening apps. A family is one sign-in on one device: its access,
-- refresh and long-lived tokens revoke together.
CREATE TABLE audio_tokens (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id         INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    hash            TEXT    NOT NULL UNIQUE,
    kind            TEXT    NOT NULL,
    family          TEXT    NOT NULL,
    app_password_id INTEGER NOT NULL DEFAULT 0,
    client          TEXT    NOT NULL DEFAULT '',
    device          TEXT    NOT NULL DEFAULT '',
    created_at      INTEGER NOT NULL,
    expires_at      INTEGER NOT NULL DEFAULT 0,
    last_used_at    INTEGER NOT NULL DEFAULT 0,
    revoked         INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX idx_audio_tokens_family ON audio_tokens(family);
CREATE INDEX idx_audio_tokens_user ON audio_tokens(user_id);

-- Durations, chapters and codec per audio file, probed once and reused until the file
-- changes (size or modification time).
CREATE TABLE audio_file_meta (
    path     TEXT    PRIMARY KEY,
    size     INTEGER NOT NULL,
    mtime    INTEGER NOT NULL,
    duration REAL    NOT NULL DEFAULT 0,
    bitrate  INTEGER NOT NULL DEFAULT 0,
    codec    TEXT    NOT NULL DEFAULT '',
    title    TEXT    NOT NULL DEFAULT '',
    chapters TEXT    NOT NULL DEFAULT '[]'
);
