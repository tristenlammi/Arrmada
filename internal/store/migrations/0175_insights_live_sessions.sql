-- 0175_insights_live_sessions: the Plex poller's in-flight streams, saved every poll.
--
-- Live sessions used to exist only in memory. Every restart (each ./update.sh) finalized
-- them at shutdown and saw them as brand-new streams on boot, so one evening's film was
-- recorded as two plays and "Now playing" went out a second time; a crash lost them
-- outright. With this table a restart picks the stream up where it left off, and a play
-- that ended while Arrmada was down is still recorded, up to when it was last seen.
--
--   started_at / last_seen_at  unix milliseconds (the poller's own clock)
--   steady, buffering, spell_counted, last_offset_ms, buf_count, buf_events
--                              the buffer-spell tracking, so a resumed stream keeps its
--                              stall history (buf_events is a JSON array)
--   snapshot                   the latest plex.Session as JSON: everything the finished
--                              play's row is built from
--
-- A row is deleted in the same transaction that records its finished play, so a crash
-- can never record a play twice or lose one.
CREATE TABLE insights_live_sessions (
    session_key    TEXT    PRIMARY KEY,
    rating_key     TEXT    NOT NULL DEFAULT '',
    user_id        TEXT    NOT NULL DEFAULT '',
    started_at     INTEGER NOT NULL,
    last_seen_at   INTEGER NOT NULL,
    paused_ms      INTEGER NOT NULL DEFAULT 0,
    state          TEXT    NOT NULL DEFAULT '',
    steady         INTEGER NOT NULL DEFAULT 0,
    buffering      INTEGER NOT NULL DEFAULT 0,
    spell_counted  INTEGER NOT NULL DEFAULT 0,
    last_offset_ms INTEGER NOT NULL DEFAULT 0,
    buf_count      INTEGER NOT NULL DEFAULT 0,
    buf_events     TEXT    NOT NULL DEFAULT '[]',
    snapshot       TEXT    NOT NULL DEFAULT '{}'
);
