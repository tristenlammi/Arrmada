-- Per-user notifications: an optional personal Apprise URL + an in-app inbox. Sourced by
-- request-ready events (a user's request was imported and is ready to watch).
ALTER TABLE users ADD COLUMN apprise_url TEXT NOT NULL DEFAULT '';

CREATE TABLE IF NOT EXISTS user_notifications (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id    INTEGER NOT NULL,
    title      TEXT    NOT NULL DEFAULT '',
    body       TEXT    NOT NULL DEFAULT '',
    media_type TEXT    NOT NULL DEFAULT '',
    ref        TEXT    NOT NULL DEFAULT '', -- e.g. "movie:12345" — used to de-dupe
    read       INTEGER NOT NULL DEFAULT 0,
    created_at INTEGER NOT NULL DEFAULT 0  -- epoch seconds
);
CREATE INDEX IF NOT EXISTS idx_user_notifications_user ON user_notifications(user_id, read);
CREATE UNIQUE INDEX IF NOT EXISTS idx_user_notifications_ref ON user_notifications(user_id, ref);
