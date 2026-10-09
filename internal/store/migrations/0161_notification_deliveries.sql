-- 0161_notification_deliveries: the alert delivery queue, which is also each connection's
-- delivery log.
--
-- Alerts used to be sent inline from the event-bus loop, one after another with a 20 s
-- apprise timeout each: a slow endpoint filled the bus buffer and later events were
-- dropped, and a failure went only to the log. Now dispatching an alert writes one row
-- per subscribed connection here and a worker sends them — a few at a time, one at a time
-- per connection, retried after 1, 5 and 30 minutes, and surviving a restart.
--
-- The URL is not copied here: the worker reads the connection when it sends, so secrets
-- stay in one table and a deleted connection's rows go with it. title/body are message
-- text only. dedupe_key ('' for none) makes a dispatch exactly-once per connection for as
-- long as the row is kept (30 days).
--
--   status  queued → sending → sent | failed (sending is reset to queued at start-up)
CREATE TABLE notification_deliveries (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    connection_id   INTEGER NOT NULL REFERENCES notifications(id) ON DELETE CASCADE,
    event_key       TEXT    NOT NULL DEFAULT '',
    title           TEXT    NOT NULL DEFAULT '',
    body            TEXT    NOT NULL DEFAULT '',
    link            TEXT    NOT NULL DEFAULT '',
    attach          TEXT    NOT NULL DEFAULT '',
    dedupe_key      TEXT    NOT NULL DEFAULT '',
    status          TEXT    NOT NULL DEFAULT 'queued',
    attempts        INTEGER NOT NULL DEFAULT 0,
    last_error      TEXT    NOT NULL DEFAULT '',
    created_at      INTEGER NOT NULL DEFAULT 0,
    next_attempt_at INTEGER NOT NULL DEFAULT 0,
    sent_at         INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX notification_deliveries_due ON notification_deliveries (status, next_attempt_at);
CREATE INDEX notification_deliveries_conn ON notification_deliveries (connection_id, id DESC);
CREATE UNIQUE INDEX notification_deliveries_dedupe ON notification_deliveries (connection_id, dedupe_key) WHERE dedupe_key != '';
