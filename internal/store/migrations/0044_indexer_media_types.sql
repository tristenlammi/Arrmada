-- Scope indexers to specific media types (movie/series/book/music). Empty = all (existing behavior).
ALTER TABLE indexers ADD COLUMN media_types TEXT NOT NULL DEFAULT '';
