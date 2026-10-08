-- Back off searching for an album no indexer carries.
--
-- The music sweep searched every incomplete monitored album every 30 minutes, against
-- every indexer, forever: an artist with twenty unfindable albums cost close to a thousand
-- searches a day, enough to trip tracker API caps that would hit Movies and Series too.
-- These record when an album was last searched and how many sweeps in a row came up empty,
-- the way books (0074) and movies/series already do.
ALTER TABLE albums ADD COLUMN last_search_at TIMESTAMP;
ALTER TABLE albums ADD COLUMN search_misses INTEGER NOT NULL DEFAULT 0;
