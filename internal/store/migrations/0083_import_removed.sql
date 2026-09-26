-- 0083_import_removed: an import whose library file the user deleted on purpose.
-- Deleting a file used to drop the import record, so the torrent — still seeding in
-- the client — looked new on the next sweep and was imported straight back. The
-- record is now kept and flagged; the importer leaves it alone until the release is
-- grabbed again.
ALTER TABLE imports ADD COLUMN removed INTEGER NOT NULL DEFAULT 0;
