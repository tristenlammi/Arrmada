-- 0157_request_ready_at: when the requester was told their request is ready.
--
-- Nothing used to record that the 'ready' notice went out, so the ready sweep re-checked
-- every approved request ever made, forever, and an upgrade of a film someone asked for
-- years ago could tell them it was ready all over again. ready_at is stamped once the
-- requester and their followers have been told (0 = not yet). It also splits approved
-- requests into the Requests page's "In progress" (0) and "Ready" (> 0) sections.
--
-- Backfill: an approved request whose ready notice is already in someone's inbox counts as
-- told, at the time of the first such notice, so nobody is re-notified after the update.
-- The ref is the one the notifier writes (requestRef): movie:<tmdb>, series:<tmdb> or
-- book:<ol_key>. Approved/declined notices carry a suffix and never match.
-- Season-scoped series requests (0164) and per-format book requests (0168) come later:
-- every row this sees is a whole-show or single-message request, so these refs are
-- exactly theirs, and the rows those migrations add start unstamped.
ALTER TABLE requests ADD COLUMN ready_at INTEGER NOT NULL DEFAULT 0;

-- MAX(1, …): a notice with no recorded time still counts as told. With no notice MIN is
-- NULL, so is the two-argument MAX, and COALESCE leaves the request untold.
UPDATE requests
   SET ready_at = COALESCE((
         SELECT MAX(1, MIN(n.created_at)) FROM user_notifications n
          WHERE n.ref = CASE WHEN requests.media_type = 'book' THEN 'book:' || requests.ol_key
                             ELSE requests.media_type || ':' || requests.tmdb_id END), 0)
 WHERE status = 'approved';

-- The Requests page's sections are plain predicates over these columns.
CREATE INDEX IF NOT EXISTS idx_requests_section ON requests(status, ready_at, updated_at);
