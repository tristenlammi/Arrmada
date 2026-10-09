-- 0154_movie_version_media: cached media info for extra version tracks.
--
-- Only the default track (movies.media_json) had its media info cached, so every view of
-- a movie page probed each extra track's file with ffprobe. Extra tracks now cache theirs
-- too, written once per import or file change. Each entry carries the file's size and
-- modification time it was read at, so a file changed outside Arrmada is noticed and read
-- again in the background.
ALTER TABLE movie_versions ADD COLUMN media_json TEXT NOT NULL DEFAULT '';
