-- Which series a book belongs to, and where it sits in it.
--
-- Book trackers carry this and Arrmada threw it away: a MyAnonaMouse listing states
-- "Coven of Bones #1", the searcher folded that into the text it scored against, and
-- nothing kept it. So a library of 91 books had no idea that seven of them were one
-- series, or that #2 was missing between the two you owned.
--
-- Position is REAL, not INTEGER: series number novellas 1.5, 2.5 and so on, and rounding
-- those into their neighbours would merge distinct books.
ALTER TABLE books ADD COLUMN series_name TEXT NOT NULL DEFAULT '';
ALTER TABLE books ADD COLUMN series_position REAL NOT NULL DEFAULT 0;

CREATE INDEX IF NOT EXISTS idx_books_series ON books(series_name, series_position);
