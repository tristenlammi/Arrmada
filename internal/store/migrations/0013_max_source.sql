-- 0013_max_source: complement min_source with an upper bound (e.g. exclude Remux).

ALTER TABLE quality_profiles ADD COLUMN max_source TEXT NOT NULL DEFAULT '';
