-- 0167_book_keys: every catalogue key a book has ever had.
--
-- A book's catalogue key (books.ol_key) changes: a Change match, the Hardcover upgrade,
-- a catalogue that answers with a canonical id other than the one asked for. Each
-- change used to forget the old key, so a Discover card still carrying it didn't read
-- "In library", a request from that card became a second request for the same book,
-- and the series and Recommended checks missed the book.
--
-- book_keys keeps them all. The book's current key is here too, so one lookup answers
-- "which book is this key". A key belongs to one book; deleting the book drops its keys.
--
--   source  'openlibrary' | 'hardcover' | 'google' — the catalogue that issued the key;
--           'request' — learned from a request linked to the book (an old key the
--           request was made under).
CREATE TABLE IF NOT EXISTS book_keys (
    key      TEXT PRIMARY KEY,
    book_id  INTEGER NOT NULL REFERENCES books(id) ON DELETE CASCADE,
    source   TEXT NOT NULL DEFAULT '',
    added_at TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE INDEX IF NOT EXISTS idx_book_keys_book ON book_keys(book_id);

-- Every book's current key.
INSERT OR IGNORE INTO book_keys (key, book_id, source)
SELECT ol_key, id,
       CASE WHEN ol_key LIKE 'hc:%' THEN 'hardcover'
            WHEN ol_key LIKE 'gb:%' THEN 'google'
            ELSE 'openlibrary' END
  FROM books
 WHERE ol_key != '';

-- The keys requests were made under, for the books they are linked to. This recovers
-- the Open Library keys of books the Hardcover upgrade has since re-keyed. A key some
-- other book already holds is left with that book (oldest request first wins the rest);
-- the boot backfill logs how many requests disagree.
INSERT OR IGNORE INTO book_keys (key, book_id, source)
SELECT ol_key, book_id, 'request'
  FROM requests
 WHERE media_type = 'book' AND book_id IS NOT NULL AND ol_key != ''
 ORDER BY id;
