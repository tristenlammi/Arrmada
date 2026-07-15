-- 0020_notifications: outbound notification connections (Discord / generic
-- webhook). Arrmada POSTs to these when it grabs or imports a release, so you
-- know what happened without watching the UI.

CREATE TABLE notifications (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    name       TEXT NOT NULL,
    kind       TEXT NOT NULL DEFAULT 'discord', -- discord | webhook
    url        TEXT NOT NULL DEFAULT '',
    on_grab    INTEGER NOT NULL DEFAULT 1,
    on_import  INTEGER NOT NULL DEFAULT 1,
    enabled    INTEGER NOT NULL DEFAULT 1,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
