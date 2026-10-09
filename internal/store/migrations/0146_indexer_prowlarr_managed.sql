-- 0146_indexer_prowlarr_managed: indexers synced from Prowlarr remember which Prowlarr
-- indexer they mirror, and who turned them off.
--
-- A re-sync used to match rows by their display name and rewrite every setting, so it
-- undid the owner's media scoping, re-enabled indexers they had switched off and left a
-- duplicate behind when an indexer was renamed in Prowlarr. prowlarr_id is the stable key
-- (0 = not from Prowlarr). disabled_by says whether the owner ('user') or a sync
-- ('prowlarr') switched a row off, so a sync only ever turns back on what a sync turned
-- off. managed_note is the one line the Indexers page shows about it ("Removed from
-- Prowlarr").
ALTER TABLE indexers ADD COLUMN prowlarr_id INTEGER NOT NULL DEFAULT 0;
ALTER TABLE indexers ADD COLUMN disabled_by TEXT NOT NULL DEFAULT '';
ALTER TABLE indexers ADD COLUMN managed_note TEXT NOT NULL DEFAULT '';
