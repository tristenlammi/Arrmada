-- 0056_seed_goal_default: stop shipping "seed forever".
--
-- ManageSeeding removes a torrent once it meets its grab's seed goal. With
-- seed_enabled=1 but seed_ratio=0 AND seed_hours=0 the goal is unreachable, so the
-- torrent is NEVER removed and its data occupies the downloads dir permanently. Those
-- were the shipped defaults (migrations 0014/0015), which meant a default install grew
-- its downloads dir without bound — on Unraid, that dir is usually the cache SSD.
--
-- Give both new and existing indexers a 14-day time goal. Time-based, not ratio-based:
-- a ratio goal can be met in minutes on a well-seeded release and remove the torrent
-- early, which can breach a private tracker's minimum-seed-time rule. A time goal
-- cannot remove early. Set either value in Settings → Indexers to override; 0/0 is
-- still accepted on edit for anyone who genuinely wants to seed indefinitely.
UPDATE indexers
   SET seed_hours = 336
 WHERE seed_enabled = 1
   AND seed_ratio = 0
   AND seed_hours = 0;

-- Grabs snapshot the seed policy at grab time (so cleanup still works if the indexer is
-- later removed or renamed), which means torrents already seeding carry the old
-- unreachable 0/0 goal and would keep seeding forever regardless of the fix above.
-- Give them the same 14-day goal. Anything already past 14 days of seed time has
-- comfortably satisfied any tracker requirement and is removed on the next cleanup pass.
UPDATE grabs
   SET seed_hours = 336
 WHERE seed_enabled = 1
   AND seed_ratio = 0
   AND seed_hours = 0;
