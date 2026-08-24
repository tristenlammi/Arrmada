-- Whether a grab was the user's own choice rather than the automation's.
--
-- The import gate compares a finished download against what's already on disk and skips
-- anything that doesn't score better. That's right for an automatic grab — it stops the
-- sweep replacing a good file with a worse one. It's wrong for a release the user picked
-- out of the interactive search or uploaded as a .torrent: they looked at the options and
-- chose that one, and the answer to "is it better" is that they said so.
ALTER TABLE grabs ADD COLUMN manual INTEGER NOT NULL DEFAULT 0;
