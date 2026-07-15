-- 0018_remap_profile_keys: movies/versions added before quality profiles became
-- editable DB rows still reference the old built-in preset keys (4k-hdr,
-- best-1080p, …). Those keys no longer resolve to a listed profile, so the UI
-- shows a raw key and scoring falls back to a permissive default. Remap them to
-- the two seeded profiles (by name) so everything points at a real profile.
-- 'n/a' (library-scanned, unmonitored) and existing 'custom:N' refs are left as-is.

UPDATE movies
SET quality_profile = 'custom:' || (SELECT id FROM quality_profiles WHERE name = '4k Sensible' AND media_type = 'movie' ORDER BY id LIMIT 1)
WHERE quality_profile IN ('4k-hdr', '4k-sane', '4k', 'remux')
  AND EXISTS (SELECT 1 FROM quality_profiles WHERE name = '4k Sensible' AND media_type = 'movie');

UPDATE movies
SET quality_profile = 'custom:' || (SELECT id FROM quality_profiles WHERE name = '1080p Sensible' AND media_type = 'movie' ORDER BY id LIMIT 1)
WHERE quality_profile IN ('best-1080p', 'smallest', '1080p', 'anything')
  AND EXISTS (SELECT 1 FROM quality_profiles WHERE name = '1080p Sensible' AND media_type = 'movie');

UPDATE movie_versions
SET quality_profile = 'custom:' || (SELECT id FROM quality_profiles WHERE name = '4k Sensible' AND media_type = 'movie' ORDER BY id LIMIT 1)
WHERE quality_profile IN ('4k-hdr', '4k-sane', '4k', 'remux')
  AND EXISTS (SELECT 1 FROM quality_profiles WHERE name = '4k Sensible' AND media_type = 'movie');

UPDATE movie_versions
SET quality_profile = 'custom:' || (SELECT id FROM quality_profiles WHERE name = '1080p Sensible' AND media_type = 'movie' ORDER BY id LIMIT 1)
WHERE quality_profile IN ('best-1080p', 'smallest', '1080p', 'anything')
  AND EXISTS (SELECT 1 FROM quality_profiles WHERE name = '1080p Sensible' AND media_type = 'movie');
