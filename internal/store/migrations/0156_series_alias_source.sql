-- 0156_series_alias_source: where an alternate title came from, and whether the owner
-- switched it off.
--
-- Aliases used to be typed by hand only. Refresh now seeds them from TMDB's alternative
-- titles (the romaji name SubsPlease and Erai-raws release under, US/UK variant titles),
-- and keeps a renamed show's old title. Those rows are source 'tmdb'.
--
--   source   'user' — typed by the owner: whole-word prefix match, pinned seasons honoured.
--            'tmdb' — added automatically: exact title match only, with the same year and
--                     country checks as the show's own title.
--   disabled 1 — an automatic alias the owner removed. Kept (not deleted) so the next
--                refresh doesn't add it straight back; ignored for matching and search.
ALTER TABLE series_aliases ADD COLUMN source TEXT NOT NULL DEFAULT 'user';
ALTER TABLE series_aliases ADD COLUMN disabled INTEGER NOT NULL DEFAULT 0;
