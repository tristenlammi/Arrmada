-- 0033_book_file_counts: track how many files each edition has, so a multi-file
-- audiobook (e.g. 100+ chapter MP3s) shows as "N files" with a collapsible list rather
-- than one confusing path. 0/1 = single file; >1 = a folder of files.

ALTER TABLE books ADD COLUMN ebook_files     INTEGER NOT NULL DEFAULT 1;
ALTER TABLE books ADD COLUMN audiobook_files INTEGER NOT NULL DEFAULT 1;
