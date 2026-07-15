-- 0023_grab_seed_rules: capture the indexer's seed policy ON the grab at grab
-- time, instead of looking it up by indexer name during cleanup. Looking it up
-- later broke whenever the originating indexer was removed or renamed — the
-- torrent could never be matched to a policy, so it seeded forever. Snapshotting
-- makes seed cleanup independent of the indexer's later fate.
--
-- Existing grabs get seed_enabled = 0 (don't seed → remove after import), which
-- also cleans up any torrents currently orphaned by a since-removed indexer.
ALTER TABLE grabs ADD COLUMN seed_enabled INTEGER NOT NULL DEFAULT 0;
ALTER TABLE grabs ADD COLUMN seed_ratio REAL NOT NULL DEFAULT 0;
ALTER TABLE grabs ADD COLUMN seed_hours INTEGER NOT NULL DEFAULT 0;
