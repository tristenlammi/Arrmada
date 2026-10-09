-- 0107_request_book_id: a book request remembers the library row it became. Requests
-- matched the library on the catalogue key they were made under, but a book's key
-- changes (the Hardcover re-match, a canonical id swap, an approve that found the book
-- under another key), and nothing updated the request: it showed "Searching" forever
-- and never sent "ready". Deleting the book clears the link rather than the request.
ALTER TABLE requests ADD COLUMN book_id INTEGER REFERENCES books(id) ON DELETE SET NULL;
CREATE INDEX IF NOT EXISTS idx_requests_book ON requests(book_id);

-- Requests whose key still names a library row link up now. The rest are matched by
-- title and author at boot (requests.Service.BackfillBookIDs), which only links a
-- unique match. Only empty links are filled, so running this again changes nothing.
UPDATE requests
   SET book_id = (SELECT id FROM books WHERE books.ol_key = requests.ol_key)
 WHERE media_type = 'book' AND book_id IS NULL AND ol_key != '';
