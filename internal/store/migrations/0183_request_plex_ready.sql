-- 0183_request_plex_ready: 'ready' waits until Plex has the title.
--
-- on_disk_at is when a movie or series request was first seen complete on disk while a
-- Plex server is set up (unix seconds, 0 = not yet, or no Plex). Its 'ready' notice
-- goes out once Plex shows the title, or after a grace period if Plex never does; until
-- then the request reads 'Adding to Plex…'. Kept in the row so a restart neither loses
-- the wait nor restarts its clock.
--
-- season_disk_at does the same for the per-season notices of a request for several
-- seasons: a JSON object of "s<season>" → unix seconds, '' when none.
ALTER TABLE requests ADD COLUMN on_disk_at INTEGER NOT NULL DEFAULT 0;
ALTER TABLE requests ADD COLUMN season_disk_at TEXT NOT NULL DEFAULT '';
