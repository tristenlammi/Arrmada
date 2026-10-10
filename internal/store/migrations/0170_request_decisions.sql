-- 0170_request_decisions: decisions with context.
--
-- A decline carried no reason and nothing recorded who decided or when, so the requester
-- only ever read "was declined" and staff couldn't tell who had approved what. A declined
-- title asked for again silently came back as a plain pending request.
--
--   decline_reason   what staff told the requester (kept on a re-request, so staff see
--                    why it was turned down before, until the next decision replaces it)
--   decided_by       the user who approved or declined it; 0 for an auto-approval
--   decided_by_name  their name at the time
--   decided_at       when, unix seconds; 0 while undecided (also keys the requester's
--                    decision notice, so a second decline after a re-request notifies again)
--   rerequest        how many times it was asked for again after a decline; > 0 marks a
--                    re-request for staff
ALTER TABLE requests ADD COLUMN decline_reason TEXT NOT NULL DEFAULT '';
ALTER TABLE requests ADD COLUMN decided_by INTEGER NOT NULL DEFAULT 0;
ALTER TABLE requests ADD COLUMN decided_by_name TEXT NOT NULL DEFAULT '';
ALTER TABLE requests ADD COLUMN decided_at INTEGER NOT NULL DEFAULT 0;
ALTER TABLE requests ADD COLUMN rerequest INTEGER NOT NULL DEFAULT 0;

-- The "New request" alert (request.created) is on by default for new connections; existing
-- admin channels get it too, chosen as 0173 chose them for the Needs-you alerts: any
-- connection that already wants grabs, imports or auto-approved requests, and every "This
-- device" push connection. A family channel that only follows Plex doesn't.
INSERT OR IGNORE INTO notification_subscriptions (connection_id, event_key)
    SELECT n.id, 'request.created' FROM notifications n
    WHERE n.kind = 'webpush' OR EXISTS (
        SELECT 1 FROM notification_subscriptions s WHERE s.connection_id = n.id AND s.event_key IN
            ('release.grabbed', 'movie.imported', 'episodes.imported', 'book.imported', 'music.imported', 'request.auto_approved'));
