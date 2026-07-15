-- 0010_profile_rules: per-profile keyword scoring, rejections, seeder floor, stall timeout.

ALTER TABLE quality_profiles ADD COLUMN keywords TEXT NOT NULL DEFAULT '[]';    -- JSON [{term, score}]
ALTER TABLE quality_profiles ADD COLUMN rejected TEXT NOT NULL DEFAULT '[]';    -- JSON [term] — hard reject (incl. file types)
ALTER TABLE quality_profiles ADD COLUMN min_seeders INTEGER NOT NULL DEFAULT 0;
ALTER TABLE quality_profiles ADD COLUMN stall_minutes INTEGER NOT NULL DEFAULT 0;
