-- 0016_movie_media: cache the default file's media info (codec, audio, HDR, …)
-- so the table view can render attributes for the whole library without probing
-- every file on each request.

ALTER TABLE movies ADD COLUMN media_json TEXT NOT NULL DEFAULT '';
