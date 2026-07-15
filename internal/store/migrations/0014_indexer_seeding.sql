-- 0014_indexer_seeding: per-indexer seed goals. When a grabbed torrent (already
-- imported) hits its ratio or time limit, Arrmada removes it — the library keeps
-- its own copy, so nothing is lost.

ALTER TABLE indexers ADD COLUMN seed_ratio REAL NOT NULL DEFAULT 0;    -- 0 = no ratio target
ALTER TABLE indexers ADD COLUMN seed_hours INTEGER NOT NULL DEFAULT 0; -- 0 = no time limit
