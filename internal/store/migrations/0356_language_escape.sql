-- 0356_language_escape: the one language escape a film's track or an episode may take.
--
-- A file whose every audio language the quality profile avoids may be replaced by a
-- lower-resolution release in a language it wants (#137). That downgrade happens at most
-- once per movie track and per episode, ever: once an import takes it, the flag is set
-- and neither the upgrade sweep nor the import gates take another, whatever the names and
-- probes of later files say — so no chain of replacements can step a file down twice.
-- The default track is the movie's own row; every other track is a movie_versions row.
-- 0 = never taken, and every row from before this column.
ALTER TABLE movies ADD COLUMN lang_escaped INTEGER NOT NULL DEFAULT 0;
ALTER TABLE movie_versions ADD COLUMN lang_escaped INTEGER NOT NULL DEFAULT 0;
ALTER TABLE episodes ADD COLUMN lang_escaped INTEGER NOT NULL DEFAULT 0;
