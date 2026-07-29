-- 0069_book_events: per-book activity timeline (added / grabbed / imported / renamed /
-- matched / failed), mirroring movie_events and series_events. Powers the History panel on
-- the book detail page — books were the only media module with no activity trail, so there
-- was no way to see why a book was grabbed, what release it came from, or when it failed.
-- Cascade-deletes with the book.

CREATE TABLE IF NOT EXISTS book_events (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    book_id    INTEGER NOT NULL REFERENCES books(id) ON DELETE CASCADE,
    event      TEXT NOT NULL,
    detail     TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_book_events_book ON book_events(book_id, id DESC);
