-- Upgrade threshold moves from "≥ N GB bigger" to "≥ N Mbps higher average
-- bitrate", which is length-independent and what users actually reason about.
-- Old GB values are meaningless as Mbps, so reset to 0 (quality-only upgrades).
ALTER TABLE quality_profiles RENAME COLUMN upgrade_gb TO upgrade_bitrate_mbps;
UPDATE quality_profiles SET upgrade_bitrate_mbps = 0;
