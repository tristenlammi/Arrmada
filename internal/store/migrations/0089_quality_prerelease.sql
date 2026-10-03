-- 0089_quality_prerelease: cams, telesyncs, telecines, screeners and workprints are refused
-- by every movie and series profile unless the profile opts in. Before this they were only
-- ranked low, so a cam was grabbed whenever it was the only release out.
ALTER TABLE quality_profiles ADD COLUMN allow_prerelease INTEGER NOT NULL DEFAULT 0;
