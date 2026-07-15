-- 0047_convert_probe_cache: persist ffprobe results for the Convert library scan so it doesn't
-- re-analyze every file on each app restart / page load. Keyed by path, validated by the file's
-- size + mtime — a cache hit is used as-is; a changed or missing file re-probes. This turns the
-- "analyzing your library" step from an every-startup ffprobe sweep into a one-time cost.

CREATE TABLE IF NOT EXISTS convert_probe_cache (
    path       TEXT    PRIMARY KEY,
    size_bytes INTEGER NOT NULL,
    mtime_unix INTEGER NOT NULL,
    info_json  TEXT    NOT NULL,
    probed_at  TEXT    NOT NULL DEFAULT (datetime('now'))
);
