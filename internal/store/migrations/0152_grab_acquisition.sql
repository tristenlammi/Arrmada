-- 0152_grab_acquisition: each grab row becomes the acquisition's live record.
--
-- Where a download had got to was re-derived on every request by matching torrent names
-- against titles, and the stall clock lived in memory, so a restart reset every one. The
-- grab row now carries what the download client last said about its torrent (by info
-- hash), so "already downloading", progress and stall fail-over read one stored record.
-- status stays the lifecycle (grabstatus.go); phase is only what the client reports:
-- queued, downloading, stalled, paused, complete, error, missing (gone from the client)...
--
--   progress / progress_at   last progress seen, and when it last moved forward (unix ms).
--                            progress_at is the stall clock, and survives a restart.
--   acq_scope                what the grab was for: v<version> (movie), S03 / S03E05 /
--                            S01-S04 / complete / abs (series), ebook / audiobook / v<id>
--                            (book), album (music). Named apart from the older `scope`
--                            column, which is the import gate's say-so for a manual pick.
--   client_id                the download client holding it.
--   last_seen_at             when the client last listed it; completed_at when it finished.
--   updated_at               when this record last changed (a phase change, mostly).
--   last_error               the latest import failure for its torrent.
--   still_waiting_at         when a stalled grab last found nothing to replace it, so the
--                            next attempt waits a window even across a restart.
ALTER TABLE grabs ADD COLUMN phase TEXT NOT NULL DEFAULT '';
ALTER TABLE grabs ADD COLUMN progress REAL NOT NULL DEFAULT 0;
ALTER TABLE grabs ADD COLUMN progress_at INTEGER NOT NULL DEFAULT 0;
ALTER TABLE grabs ADD COLUMN acq_scope TEXT NOT NULL DEFAULT '';
ALTER TABLE grabs ADD COLUMN client_id INTEGER NOT NULL DEFAULT 0;
ALTER TABLE grabs ADD COLUMN last_seen_at INTEGER NOT NULL DEFAULT 0;
ALTER TABLE grabs ADD COLUMN completed_at INTEGER NOT NULL DEFAULT 0;
ALTER TABLE grabs ADD COLUMN updated_at INTEGER NOT NULL DEFAULT 0;
ALTER TABLE grabs ADD COLUMN last_error TEXT NOT NULL DEFAULT '';
ALTER TABLE grabs ADD COLUMN still_waiting_at INTEGER NOT NULL DEFAULT 0;

-- "What's in flight for this title" is asked per sweep and per page load.
CREATE INDEX idx_grabs_item_status ON grabs(media_type, movie_id, status);
