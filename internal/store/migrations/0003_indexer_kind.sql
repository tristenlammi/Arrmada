-- 0003_indexer_kind: generalize indexers beyond the torznab/newznab protocols
-- so native trackers (e.g. TorrentLeech) with login credentials fit the model.
-- SQLite can't relax the old protocol CHECK in place, so rebuild the table.

CREATE TABLE indexers_new (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    name       TEXT NOT NULL,
    kind       TEXT NOT NULL DEFAULT 'torznab',  -- torznab | newznab | torrentleech
    url        TEXT NOT NULL DEFAULT '',
    api_key    TEXT NOT NULL DEFAULT '',
    username   TEXT NOT NULL DEFAULT '',
    password   TEXT NOT NULL DEFAULT '',
    categories TEXT NOT NULL DEFAULT '',
    priority   INTEGER NOT NULL DEFAULT 25,
    enabled    INTEGER NOT NULL DEFAULT 1,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- Carry over existing rows; the old 'protocol' becomes 'kind'.
INSERT INTO indexers_new (id, name, kind, url, api_key, categories, priority, enabled, created_at)
    SELECT id, name, protocol, url, api_key, categories, priority, enabled, created_at FROM indexers;

DROP TABLE indexers;
ALTER TABLE indexers_new RENAME TO indexers;
CREATE INDEX idx_indexers_enabled ON indexers(enabled);
