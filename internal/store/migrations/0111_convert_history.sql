-- 0111_convert_history: a durable ledger of every conversion outcome. The in-memory job
-- list is capped and empty after a restart, and the activity log is free text; this keeps
-- the before/after spec, sizes, quality scores, crop and track decisions per file. Converted
-- rows are kept for good (they are the record of what was changed); other outcomes are
-- pruned after 90 days. The hold_* columns are for the originals hold, filled in later.
CREATE TABLE IF NOT EXISTS convert_history (
    id                  INTEGER PRIMARY KEY AUTOINCREMENT,
    item_key            TEXT    NOT NULL,
    kind                TEXT    NOT NULL,
    movie_id            INTEGER NOT NULL DEFAULT 0,
    version_id          INTEGER NOT NULL DEFAULT 0,
    series_id           INTEGER NOT NULL DEFAULT 0,
    season              INTEGER NOT NULL DEFAULT 0,
    episode             INTEGER NOT NULL DEFAULT 0,
    title               TEXT    NOT NULL DEFAULT '',
    outcome             TEXT    NOT NULL,             -- in_progress | done | failed | skipped | cancelled
    outcome_kind        TEXT    NOT NULL DEFAULT '',  -- the skip kind, when skipped
    note                TEXT    NOT NULL DEFAULT '',
    requested           INTEGER NOT NULL DEFAULT 0,
    src_path            TEXT    NOT NULL DEFAULT '',
    src_release         TEXT    NOT NULL DEFAULT '',
    src_size            INTEGER NOT NULL DEFAULT 0,
    src_info_json       TEXT    NOT NULL DEFAULT '',
    out_path            TEXT    NOT NULL DEFAULT '',
    out_size            INTEGER NOT NULL DEFAULT 0,
    out_info_json       TEXT    NOT NULL DEFAULT '',
    codec               TEXT    NOT NULL DEFAULT '',
    crf                 INTEGER NOT NULL DEFAULT 0,
    encoder             TEXT    NOT NULL DEFAULT '',
    pix_fmt             TEXT    NOT NULL DEFAULT '',
    ssim_mean           REAL    NOT NULL DEFAULT 0,
    ssim_min            REAL    NOT NULL DEFAULT 0,
    ssim_windows_json   TEXT    NOT NULL DEFAULT '',
    vmaf_mean           REAL    NOT NULL DEFAULT 0,
    crop                TEXT    NOT NULL DEFAULT '',
    kept_tracks_json    TEXT    NOT NULL DEFAULT '',
    dropped_tracks_json TEXT    NOT NULL DEFAULT '',
    warnings_json       TEXT    NOT NULL DEFAULT '',
    reclaim_deferred    INTEGER NOT NULL DEFAULT 0,
    hold_path           TEXT    NOT NULL DEFAULT '',
    hold_until          INTEGER NOT NULL DEFAULT 0,
    hold_state          TEXT    NOT NULL DEFAULT '',
    released_at         INTEGER NOT NULL DEFAULT 0,
    reverted_at         INTEGER NOT NULL DEFAULT 0,
    started_at          INTEGER NOT NULL DEFAULT 0,
    finished_at         INTEGER NOT NULL DEFAULT 0,
    encode_secs         REAL    NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_convert_history_item ON convert_history (item_key, finished_at);
CREATE INDEX IF NOT EXISTS idx_convert_history_finished ON convert_history (finished_at);
CREATE INDEX IF NOT EXISTS idx_convert_history_hold ON convert_history (hold_state);
