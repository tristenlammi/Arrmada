-- 0164_request_seasons: a series request can ask for some seasons, and a show can have
-- several requests (S1-3 last year, S4 now). seasons holds a JSON array of season
-- numbers; '' means the whole show, which every request made before this one keeps.
-- One request per movie stays enforced; series lose their unique index (0034 made it a
-- partial index, so no table rebuild is needed). Books keep idx_requests_ol.
ALTER TABLE requests ADD COLUMN seasons TEXT NOT NULL DEFAULT '';

DROP INDEX IF EXISTS idx_requests_tmdb;
CREATE UNIQUE INDEX IF NOT EXISTS idx_requests_movie ON requests(tmdb_id) WHERE media_type = 'movie';
CREATE INDEX IF NOT EXISTS idx_requests_series ON requests(tmdb_id) WHERE media_type = 'series';
