-- 0039_quality_bitrate_ceiling: the quality profile's size ceiling becomes a *bitrate* ceiling.
-- A fixed GB cap over-penalises long movies / full seasons and barely limits short ones; a bitrate
-- (Mbps) cap is length-independent — it's the actual quality-per-minute measure. Rename the column
-- and convert any existing GB cap to an approximate Mbps using a nominal 120-minute runtime
-- (GiB × 8589.93 ÷ (120 × 60) ≈ GiB × 1.193). A 0 cap ("no limit") stays 0.

ALTER TABLE quality_profiles RENAME COLUMN size_cap_gb TO bitrate_cap_mbps;

UPDATE quality_profiles
   SET bitrate_cap_mbps = ROUND(bitrate_cap_mbps * 8589.934592 / (120 * 60.0), 1)
 WHERE bitrate_cap_mbps > 0;
