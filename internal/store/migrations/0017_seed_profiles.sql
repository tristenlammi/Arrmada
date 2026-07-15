-- 0017_seed_profiles: ship two starter quality profiles as ordinary editable rows.
-- There are no built-in/locked profiles anymore — every profile is a custom one.
-- These two just come pre-loaded to help; users can edit or delete them freely.

INSERT INTO quality_profiles (media_type, name, base, allowed_resolutions, size_cap_gb, small_bias, format_scores)
VALUES
    ('movie', '1080p Sensible', '', '["1080p"]', 20, 0.3, '{"Atmos":30,"Dolby Vision":20,"HDR10":15}'),
    ('movie', '4k Sensible',    '', '["2160p"]', 40, 0.3, '{"Dolby Vision":55,"HDR10":45,"Atmos":40}');
