-- 0184_notification_plex_url: a 'ready' notice for a title the owner's Plex has keeps the
-- title's app.plex.tv page, so the bell can offer Watch on Plex. Empty for every other
-- notice. Where a notice leads inside Arrmada is worked out from its ref, as before.
ALTER TABLE user_notifications ADD COLUMN plex_url TEXT NOT NULL DEFAULT '';
