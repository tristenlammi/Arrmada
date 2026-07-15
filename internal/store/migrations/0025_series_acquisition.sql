-- 0025_series_acquisition: series share the grabs table (for seed cleanup / stall
-- detection); media_type disambiguates a movie grab from a series grab (movie_id
-- holds the series id when media_type='series'). Also seed the two starter quality
-- profiles for series (no size cap — a season pack's total size shouldn't trip a
-- per-movie-sized ceiling; per-episode limits come later).

ALTER TABLE grabs ADD COLUMN media_type TEXT NOT NULL DEFAULT 'movie';

INSERT INTO quality_profiles (media_type, name, base, allowed_resolutions, size_cap_gb, small_bias, format_scores)
VALUES
    ('series', '1080p Sensible', '', '["1080p"]', 0, 0.3, '{"Atmos":30,"Dolby Vision":20,"HDR10":15}'),
    ('series', '4k Sensible',    '', '["2160p"]', 0, 0.3, '{"Dolby Vision":55,"HDR10":45,"Atmos":40}');
