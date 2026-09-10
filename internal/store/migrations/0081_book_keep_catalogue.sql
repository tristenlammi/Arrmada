-- 0081_book_keep_catalogue: a book the user has told the Hardcover re-match to leave
-- alone. Counted out of "still on Open Library keys" and skipped by the re-match, so a
-- book the catalogue simply doesn't have stops being reported every run.
ALTER TABLE books ADD COLUMN keep_catalogue INTEGER NOT NULL DEFAULT 0;
