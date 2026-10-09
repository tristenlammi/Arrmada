-- 0102_import_attach_state: whether a recorded import has been attached to its movie.
--
-- Attaching (Wanted → Downloaded, the request-ready message, Convert and Subtitles
-- indexing) used to hang off a best-effort bus event. A dropped event, or a restart
-- between recording the import and attaching it, left the movie Wanted for good: the
-- recorded import was never looked at again. Now the import is recorded as 'pending',
-- attached in the same sweep, and retried from these columns until it lands.
--
-- attach_state: 'pending' (retried), or a final 'attached' / 'unmatched' (no movie in
-- the library fits) / 'refused' (the quality gate kept the better file already there) /
-- 'gone' (the imported file disappeared before it could be attached). Rows from before
-- this migration were attached by the old path, so they default to 'attached' and are
-- never retried.
ALTER TABLE imports ADD COLUMN attach_state TEXT NOT NULL DEFAULT 'attached';
ALTER TABLE imports ADD COLUMN attach_attempts INTEGER NOT NULL DEFAULT 0;
ALTER TABLE imports ADD COLUMN attach_error TEXT NOT NULL DEFAULT '';
ALTER TABLE imports ADD COLUMN attach_next_at INTEGER NOT NULL DEFAULT 0; -- unix seconds
-- What the retry needs that the row didn't keep: the release name (scored for upgrades,
-- stamped on the movie) and the year the title was matched with.
ALTER TABLE imports ADD COLUMN release_name TEXT NOT NULL DEFAULT '';
ALTER TABLE imports ADD COLUMN year INTEGER NOT NULL DEFAULT 0;

CREATE INDEX imports_attach_pending ON imports (attach_next_at) WHERE attach_state = 'pending';
