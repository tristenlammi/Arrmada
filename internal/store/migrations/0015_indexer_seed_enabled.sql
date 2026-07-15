-- 0015_indexer_seed_enabled: explicit seed-rules switch.
--   seed_enabled = 0 → remove the torrent as soon as it's imported (no seeding)
--   seed_enabled = 1 → seed per seed_ratio / seed_hours (both 0 = seed forever)

ALTER TABLE indexers ADD COLUMN seed_enabled INTEGER NOT NULL DEFAULT 1;
