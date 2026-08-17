-- Fold the owner's placeholder user row into their real Plex account row.
--
-- A Plex Media Server reports the server owner as user id "1" in /status/sessions — a
-- placeholder meaning "whoever owns this box". Every other source of watch history,
-- Tautulli included, records the owner's real plex.tv account id. So the live poller and
-- an imported history filed the same person under two ids, and the Users tab listed them
-- twice with their plays and watch time split between the rows.
--
-- The runtime fix resolves the real id from plex.tv and records under it from now on (see
-- ownerID in the insights service). This migration repairs what's already stored.
--
-- Matching is by username, and only when it is UNAMBIGUOUS: exactly one non-placeholder
-- row carries the same username. Anything else is left alone for the runtime reconcile,
-- which knows the real id rather than inferring it.

-- Carry the placeholder's last-seen forward, so merging can't move the surviving row's
-- timestamp backwards.
UPDATE plex_users
   SET last_seen_at = MAX(last_seen_at, COALESCE((SELECT o.last_seen_at FROM plex_users o WHERE o.id = '1'), 0))
 WHERE id <> '1'
   AND username = (SELECT o.username FROM plex_users o WHERE o.id = '1')
   AND (SELECT COUNT(*) FROM plex_users r, plex_users o
         WHERE o.id = '1' AND r.id <> '1' AND r.username = o.username) = 1;

UPDATE stream_sessions
   SET user_id = (SELECT r.id FROM plex_users r, plex_users o
                   WHERE o.id = '1' AND r.id <> '1' AND r.username = o.username)
 WHERE user_id = '1'
   AND (SELECT COUNT(*) FROM plex_users r, plex_users o
         WHERE o.id = '1' AND r.id <> '1' AND r.username = o.username) = 1;

-- Drop the placeholder only once nothing points at it, so a partial or skipped merge
-- above can never orphan sessions.
DELETE FROM plex_users
 WHERE id = '1'
   AND NOT EXISTS (SELECT 1 FROM stream_sessions WHERE user_id = '1');
