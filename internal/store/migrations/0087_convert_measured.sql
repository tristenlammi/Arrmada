-- 0087_convert_measured: what a test encode measured, so the Library can show a file's
-- real expected size instead of the cautious estimate once it has been tested.
--
-- One row per file and format (Compare measures HEVC and AV1 both). Keyed to the file's
-- size as well as its path: a replaced file has to be measured afresh.
CREATE TABLE IF NOT EXISTS convert_measured (
    path        TEXT    NOT NULL,
    codec       TEXT    NOT NULL,
    size_bytes  INTEGER NOT NULL,
    crf         INTEGER NOT NULL,
    video_bytes INTEGER NOT NULL, -- the whole file's video at that setting, projected
    ssim        REAL    NOT NULL DEFAULT 0,
    source      TEXT    NOT NULL DEFAULT '', -- "test encode" | "compare"
    measured_at INTEGER NOT NULL,
    PRIMARY KEY (path, codec)
);
