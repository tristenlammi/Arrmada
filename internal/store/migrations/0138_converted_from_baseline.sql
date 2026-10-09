-- 0138_converted_from_baseline: what a converted file was before Convert shrank it.
--
-- After a conversion the record points at a file a fraction of the original's size, and
-- the upgrade sweep judged it as that file: a remux of the same film, from any group, then
-- looked like a large bitrate upgrade, so Arrmada could re-download the very kind of file
-- it had just converted, and convert that again, for ever. These columns keep the release
-- and size the file had before its first conversion. Upgrades must beat THAT; a
-- re-conversion keeps the first original; a real import clears them.
--
-- converted_from_size is bytes; 0 means unknown (the current size is used).
ALTER TABLE movies ADD COLUMN converted_from_release TEXT NOT NULL DEFAULT '';
ALTER TABLE movies ADD COLUMN converted_from_size INTEGER NOT NULL DEFAULT 0;
ALTER TABLE movie_versions ADD COLUMN converted_from_release TEXT NOT NULL DEFAULT '';
ALTER TABLE movie_versions ADD COLUMN converted_from_size INTEGER NOT NULL DEFAULT 0;
ALTER TABLE episodes ADD COLUMN converted_from_release TEXT NOT NULL DEFAULT '';
ALTER TABLE episodes ADD COLUMN converted_from_size INTEGER NOT NULL DEFAULT 0;

-- Releases Convert stamped the old way still end in an appended " x265" or " AV1" (the
-- boot repair rewrites them in place afterwards): the name before the stamp is what the
-- file was converted from. Case-sensitive on purpose (GLOB), so a release that merely
-- ends in "av1" is left alone. Size stays 0: it isn't known here. Conversions recorded in
-- the Convert ledger are backfilled with their sizes at boot (repair:converted_from_v1).
UPDATE movies SET converted_from_release = substr(source_release, 1, length(source_release) - 5)
 WHERE converted_from_release = '' AND source_release GLOB '* x265';
UPDATE movies SET converted_from_release = substr(source_release, 1, length(source_release) - 4)
 WHERE converted_from_release = '' AND source_release GLOB '* AV1';
UPDATE movie_versions SET converted_from_release = substr(source_release, 1, length(source_release) - 5)
 WHERE converted_from_release = '' AND source_release GLOB '* x265';
UPDATE movie_versions SET converted_from_release = substr(source_release, 1, length(source_release) - 4)
 WHERE converted_from_release = '' AND source_release GLOB '* AV1';
UPDATE episodes SET converted_from_release = substr(source_release, 1, length(source_release) - 5)
 WHERE converted_from_release = '' AND source_release GLOB '* x265';
UPDATE episodes SET converted_from_release = substr(source_release, 1, length(source_release) - 4)
 WHERE converted_from_release = '' AND source_release GLOB '* AV1';
