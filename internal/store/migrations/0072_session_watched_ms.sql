-- Separate "how long they actually watched" from "how long the media runs".
--
-- duration_ms carried BOTH meanings depending on who wrote the row. The live poller
-- stores Plex's media runtime there (activity.go divides view_offset_ms by it to get a
-- progress percentage, which only makes sense against the runtime). The Tautulli import
-- stored Tautulli's `duration` field — which is watched SECONDS, not the runtime.
--
-- With no honest watched figure to read, every aggregate fell back to wall-clock:
-- (stopped_at - started_at) - paused_ms/1000. For a live-tracked session that's accurate,
-- because the poller sees the start and the stop. For an imported Tautulli row it counts
-- every minute a client sat idle without sending a stop, which is why the Users tab showed
-- 24-34 hours per play against a real average nearer half an hour.
ALTER TABLE stream_sessions ADD COLUMN watched_ms INTEGER NOT NULL DEFAULT 0;

-- Backfill the imported rows. They're identifiable: session_key is assigned by the live
-- poller from Plex's own key, so an empty one means the row came from an import, and its
-- duration_ms is watched time wearing the wrong name.
UPDATE stream_sessions
   SET watched_ms = duration_ms
 WHERE session_key = '' AND duration_ms > 0;

-- Having moved it, clear the field it was squatting in: we don't know these rows' media
-- runtime, and leaving watched time there makes History report a nonsense progress
-- percentage (offset ÷ watched rather than offset ÷ runtime).
UPDATE stream_sessions
   SET duration_ms = 0
 WHERE session_key = '' AND watched_ms > 0;
