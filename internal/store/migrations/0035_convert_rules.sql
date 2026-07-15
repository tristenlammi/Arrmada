-- 0035_convert_rules: the Convert module's rules engine (C2). A rule targets library files
-- by simple filters (source codec / minimum resolution / minimum size) and applies the
-- "Save space" action (→ HEVC). Rules with auto=1 are swept on a schedule. Matches are
-- computed at read time against the live library (probed specs), so nothing is cached here.

CREATE TABLE IF NOT EXISTS convert_rules (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    name           TEXT NOT NULL,
    enabled        INTEGER NOT NULL DEFAULT 1,
    codecs         TEXT NOT NULL DEFAULT '',   -- CSV of source codecs to match; empty = any convertible
    min_height     INTEGER NOT NULL DEFAULT 0, -- only files with height >= this (0 = any)
    min_size_bytes INTEGER NOT NULL DEFAULT 0, -- only files at least this big (0 = any)
    auto           INTEGER NOT NULL DEFAULT 0, -- included in the scheduled sweep
    created_at     TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
