-- 0011_indexer_seeders: per-indexer minimum-seeder floor for torrent results.

ALTER TABLE indexers ADD COLUMN min_seeders INTEGER NOT NULL DEFAULT 0;
