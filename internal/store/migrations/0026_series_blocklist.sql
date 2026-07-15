-- 0026_series_blocklist: series share the blocklist table (used by stall fail-over so a
-- re-search doesn't re-grab the release that just stalled). media_type disambiguates a
-- movie block from a series block — movie_id holds the series id when media_type='series',
-- mirroring how the grabs table is shared.

ALTER TABLE blocklist ADD COLUMN media_type TEXT NOT NULL DEFAULT 'movie';
