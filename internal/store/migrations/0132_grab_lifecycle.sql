-- 0132_grab_lifecycle: a review's outcome, and grabs that follow it.
--
-- A grab's row decides whether its torrent is re-grabbed, stall-checked or removed at its
-- seed goal. Review decisions never touched it, so a series imported through Review sat at
-- 'grabbed' and seeded forever, and requesters saw "Importing" indefinitely. Grabs now go
-- 'held' while their review is pending, then 'imported', 'dismissed' or 'failed' when it
-- is resolved; the review records how it was settled and when.
ALTER TABLE import_reviews ADD COLUMN resolution TEXT NOT NULL DEFAULT '';
ALTER TABLE import_reviews ADD COLUMN resolved_at TIMESTAMP;

-- Downloads waiting in Review right now.
UPDATE grabs SET status = 'held'
 WHERE status = 'grabbed' AND info_hash != ''
   AND lower(info_hash) IN (SELECT lower(hash) FROM import_reviews WHERE status = 'pending' AND hash != '');

-- Old reviews already settled whose grab was left at 'grabbed'. Which way each went wasn't
-- recorded, so they become 'dismissed': seed cleanup then removes the torrent at its goal
-- with its files kept, which is safe whether the content was imported or not.
UPDATE grabs SET status = 'dismissed'
 WHERE status = 'grabbed' AND info_hash != ''
   AND lower(info_hash) IN (SELECT lower(hash) FROM import_reviews WHERE status = 'resolved' AND hash != '')
   AND lower(info_hash) IN (SELECT lower(download_hash) FROM imports);
