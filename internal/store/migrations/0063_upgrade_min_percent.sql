-- The upgrade threshold moves from "≥ N Mbps higher" to "≥ N% better", because a
-- percentage is the only unit a user can reason about without already knowing what
-- bitrate their files are.
--
-- "2 Mbps better" means something completely different at 480p (~1.5 Mbps, so more than
-- doubling it) than at 2160p (~40 Mbps, where it's noise). A percentage is the same
-- promise at every resolution — and it's what the guard rail against churn was already
-- expressed as internally, so the setting now says what the code actually does.
--
-- Existing Mbps values can't be converted: the same number is a different proportion for
-- every file it would be compared against. Anyone who had the feature ON is moved to 25%,
-- the mildest setting that still means "noticeably better"; OFF stays off.
ALTER TABLE quality_profiles RENAME COLUMN upgrade_bitrate_mbps TO upgrade_min_percent;
UPDATE quality_profiles SET upgrade_min_percent = CASE WHEN upgrade_min_percent > 0 THEN 25 ELSE 0 END;
