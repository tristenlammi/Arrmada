-- Back off searching for a book no indexer carries.
--
-- Movies and series both record when a sweep last ran and how many consecutive times it
-- came up empty, then wait longer after each miss. The books sweep had neither, so every
-- monitored-but-missing book was searched every 30 minutes forever, against every
-- indexer — a book nobody has ever uploaded cost the same as one about to appear.
ALTER TABLE books ADD COLUMN last_search_at TIMESTAMP;
ALTER TABLE books ADD COLUMN search_misses INTEGER NOT NULL DEFAULT 0;
