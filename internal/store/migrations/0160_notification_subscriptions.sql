-- 0160_notification_subscriptions: which alert events each connection wants, by event key.
--
-- Connections used to carry one column per event (on_grab, on_import, on_stream,
-- on_buffering), so every new kind of alert meant a migration and another toggle. The
-- alert catalog (internal/notify/catalog.go) now names events by key, and a connection
-- subscribes to any set of them here.
--
-- Backfill keeps every connection's previous choices. "Imported" covered movies and TV
-- episodes; book imports had no alert at all, so connections that wanted imports get
-- those too.
--
-- The old columns stay (a rolled-back build still reads them, and the app keeps them in
-- step for the four events they describe), but nothing reads them any more.
CREATE TABLE notification_subscriptions (
    connection_id INTEGER NOT NULL REFERENCES notifications(id) ON DELETE CASCADE,
    event_key     TEXT    NOT NULL,
    PRIMARY KEY (connection_id, event_key)
);

INSERT INTO notification_subscriptions (connection_id, event_key)
    SELECT id, 'release.grabbed' FROM notifications WHERE on_grab != 0;
INSERT INTO notification_subscriptions (connection_id, event_key)
    SELECT id, 'movie.imported' FROM notifications WHERE on_import != 0;
INSERT INTO notification_subscriptions (connection_id, event_key)
    SELECT id, 'episodes.imported' FROM notifications WHERE on_import != 0;
INSERT INTO notification_subscriptions (connection_id, event_key)
    SELECT id, 'book.imported' FROM notifications WHERE on_import != 0;
INSERT INTO notification_subscriptions (connection_id, event_key)
    SELECT id, 'plex.stream.started' FROM notifications WHERE on_stream != 0;
INSERT INTO notification_subscriptions (connection_id, event_key)
    SELECT id, 'plex.buffering' FROM notifications WHERE on_buffering != 0;
