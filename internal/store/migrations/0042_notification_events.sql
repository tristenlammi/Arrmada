-- Notifications now deliver via Apprise and gain Plex watch-event triggers.
ALTER TABLE notifications ADD COLUMN on_stream INTEGER NOT NULL DEFAULT 0;
ALTER TABLE notifications ADD COLUMN on_buffering INTEGER NOT NULL DEFAULT 0;
