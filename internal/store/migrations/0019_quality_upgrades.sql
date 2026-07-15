-- 0019_quality_upgrades: automatic quality upgrades.
--
-- source_release records the release name a file was imported from. We need it
-- because ffprobe can read a file's resolution/HDR but NOT its Atmos/TrueHD
-- (those live inside the audio stream) — the release name is the only reliable
-- signal of the full quality of the file we already have, so upgrade decisions
-- score against it.
ALTER TABLE movies ADD COLUMN source_release TEXT NOT NULL DEFAULT '';
ALTER TABLE movie_versions ADD COLUMN source_release TEXT NOT NULL DEFAULT '';

-- Per-profile upgrade behavior:
--   upgrades_enabled — keep looking for a better release after a file is imported.
--   upgrade_gb       — also upgrade when a release is at least this many GB bigger
--                      than the current file AND at least as good on quality/format
--                      (0 = only upgrade on a genuine quality/format improvement,
--                      never on size/bitrate alone).
ALTER TABLE quality_profiles ADD COLUMN upgrades_enabled INTEGER NOT NULL DEFAULT 1;
ALTER TABLE quality_profiles ADD COLUMN upgrade_gb REAL NOT NULL DEFAULT 0;
