-- 0002_indexers: configured Torznab/Newznab search sources.

CREATE TABLE indexers (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    name       TEXT NOT NULL,
    protocol   TEXT NOT NULL CHECK (protocol IN ('torznab', 'newznab')),
    url        TEXT NOT NULL,
    api_key    TEXT NOT NULL DEFAULT '',
    categories TEXT NOT NULL DEFAULT '',       -- CSV of category ids
    priority   INTEGER NOT NULL DEFAULT 25,    -- 1 (highest) .. 50 (lowest)
    enabled    INTEGER NOT NULL DEFAULT 1,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX idx_indexers_enabled ON indexers(enabled);
