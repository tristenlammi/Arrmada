-- 0173_attention_state: what the Needs-you alerter has seen and said, by attention key.
--
-- The alerter diffs every attention refresh against this table, so an alert goes out once
-- per problem and a restart doesn't repeat it: most items' "since" is only known to the
-- running process (a health finding, an errored torrent), so what was announced has to be
-- kept here rather than worked out again from the feed.
--
--   first_seen    when this occurrence started (unix ms); part of every dedupe key, so a
--                 problem that clears and comes back much later is a new alert
--   runs/misses   refreshes in a row it has been present / absent (settling and resolving)
--   told          what the owner last heard: '' nothing, 'problem', 'resolved', or 'quiet'
--                 (already there when alerts were first switched on, so never announced)
--   alerted_at    when its problem alert was last sent (unix ms) — the 6-hour flap guard
--   resolved_at   when it cleared (unix ms); 0 while it is still there
--   pending_*     an alert decided but not yet handed to the delivery queue; the next
--                 refresh hands it over again (the dedupe key keeps that exactly-once)
CREATE TABLE attention_state (
    key            TEXT    PRIMARY KEY,
    kind           TEXT    NOT NULL,
    event          TEXT    NOT NULL DEFAULT '',
    level          TEXT    NOT NULL DEFAULT 'warning',
    name           TEXT    NOT NULL DEFAULT '',
    title          TEXT    NOT NULL DEFAULT '',
    detail         TEXT    NOT NULL DEFAULT '',
    link           TEXT    NOT NULL DEFAULT '',
    first_seen     INTEGER NOT NULL,
    last_seen      INTEGER NOT NULL,
    runs           INTEGER NOT NULL DEFAULT 0,
    misses         INTEGER NOT NULL DEFAULT 0,
    told           TEXT    NOT NULL DEFAULT '',
    alerted_at     INTEGER NOT NULL DEFAULT 0,
    resolved_at    INTEGER NOT NULL DEFAULT 0,
    pending_event  TEXT    NOT NULL DEFAULT '',
    pending_dedupe TEXT    NOT NULL DEFAULT ''
);

-- Existing admin channels get the Needs-you alerts that are on by default: any connection
-- that already wants grabs, imports or auto-approved requests, and every "This device"
-- push connection. A family channel that only follows Plex doesn't.
INSERT OR IGNORE INTO notification_subscriptions (connection_id, event_key)
    SELECT n.id, e.key FROM notifications n
    JOIN (SELECT 'import.held' AS key UNION ALL SELECT 'import.stuck' UNION ALL SELECT 'download.failed'
          UNION ALL SELECT 'health.problem' UNION ALL SELECT 'health.resolved') e
    WHERE n.kind = 'webpush' OR EXISTS (
        SELECT 1 FROM notification_subscriptions s WHERE s.connection_id = n.id AND s.event_key IN
            ('release.grabbed', 'movie.imported', 'episodes.imported', 'book.imported', 'music.imported', 'request.auto_approved'));
