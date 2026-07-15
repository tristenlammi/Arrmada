-- 0005_imports: record of downloads imported into the library (also the dedupe
-- key so a completed download is only imported once).

CREATE TABLE imports (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    download_hash TEXT NOT NULL UNIQUE,
    source_path   TEXT NOT NULL,
    target_path   TEXT NOT NULL,
    title         TEXT NOT NULL DEFAULT '',
    size_bytes    INTEGER NOT NULL DEFAULT 0,
    imported_at   TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
