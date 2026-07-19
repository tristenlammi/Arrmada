-- 0058_convert_library_index: a persisted index of every convertible library file.
--
-- The Convert library list used to be computed inside the HTTP request: for TV it ran
-- series.Get per series (N+1, loading all seasons and episodes), then os.Stat AND a
-- probe-cache SELECT per episode, then returned every episode in one JSON array. At a
-- few thousand episodes that's thousands of filesystem stats — which on Unraid's
-- /mnt/user FUSE spins up array disks — thousands of DB round-trips, a multi-MB payload,
-- and a DOM with thousands of rows. Hence the 120s timeout on the handler.
--
-- This table holds the answer instead, so listing is one indexed query.
--
-- Deliberately stores RAW FACTS, not "is this a convert candidate". Candidacy is just
-- video_codec != target (see isCandidateFor), so it's computed in the query — switching
-- the target codec from HEVC to AV1 needs no rescan.
--
-- size_bytes mirrors what movies/episodes already record, so the indexer can tell an
-- unchanged file from a changed one WITHOUT touching the filesystem. Unchanged files are
-- never stat'd or re-probed, which is what keeps the sweep from waking every array disk.
CREATE TABLE IF NOT EXISTS convert_library (
    path         TEXT PRIMARY KEY,
    media_type   TEXT    NOT NULL,            -- 'movie' | 'episode'
    movie_id     INTEGER NOT NULL DEFAULT 0,
    series_id    INTEGER NOT NULL DEFAULT 0,
    season       INTEGER NOT NULL DEFAULT 0,
    episode      INTEGER NOT NULL DEFAULT 0,
    title        TEXT    NOT NULL DEFAULT '', -- display title, denormalized for listing
    year         INTEGER NOT NULL DEFAULT 0,
    poster_url   TEXT    NOT NULL DEFAULT '',
    size_bytes   INTEGER NOT NULL DEFAULT 0,
    video_codec  TEXT    NOT NULL DEFAULT '',
    info_json    TEXT    NOT NULL DEFAULT '', -- full MediaInfo, so the detail view needs no probe
    indexed_at   TEXT    NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS idx_convert_library_series ON convert_library(series_id, season, episode);
CREATE INDEX IF NOT EXISTS idx_convert_library_media  ON convert_library(media_type);
CREATE INDEX IF NOT EXISTS idx_convert_library_codec  ON convert_library(video_codec);
