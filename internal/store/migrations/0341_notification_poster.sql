-- 0341_notification_poster: a 'ready' notice for a movie or show keeps the title's poster
-- (the public TMDB image URL the library already stores), so the bell can show it beside the
-- words. Empty for every other notice, for anything the adult filter hides, and for every
-- notice sent before this column existed.
ALTER TABLE user_notifications ADD COLUMN poster_url TEXT NOT NULL DEFAULT '';
