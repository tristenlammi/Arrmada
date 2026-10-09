-- 0141_upgrade_hold: "keep existing files" when a profile changes.
--
-- Saving a stricter profile used to mean every file that no longer met it became fair
-- game for the upgrade sweeps. upgrade_hold = 1 keeps a file out of profile-driven
-- upgrades (the sweeps skip it, and the TV import gate won't replace it on its own) while
-- the profile applies to everything grabbed from now on. Missing files are still searched
-- for. The hold ends when a new file is imported for that row, when the title moves to
-- another profile, or with Resume upgrades; a convert or rename keeps it.
ALTER TABLE movies ADD COLUMN upgrade_hold INTEGER NOT NULL DEFAULT 0;
ALTER TABLE movie_versions ADD COLUMN upgrade_hold INTEGER NOT NULL DEFAULT 0;
ALTER TABLE episodes ADD COLUMN upgrade_hold INTEGER NOT NULL DEFAULT 0;
