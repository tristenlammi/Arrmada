-- 0049_subtitle_probe_cache: persist ffprobe results for the Subtitles module's library scan so it
-- doesn't re-analyze every file on each page load. Keyed by path, validated by the file's size +
-- mtime (a changed/missing file re-probes). Same pattern as convert_probe_cache.

CREATE TABLE IF NOT EXISTS subtitle_probe_cache (
    path       TEXT    PRIMARY KEY,
    size_bytes INTEGER NOT NULL,
    mtime_unix INTEGER NOT NULL,
    info_json  TEXT    NOT NULL,
    probed_at  TEXT    NOT NULL DEFAULT (datetime('now'))
);
