-- 0082_book_audio_versions: extra audiobook versions of one book — a full-cast
-- GraphicAudio production next to the standard narration, a second narrator, anything
-- the user names. The book's own audiobook_* columns stay the standard version; each
-- row here is one more, with the words a release must mention to count as it.
CREATE TABLE book_audio_versions (
    id        INTEGER PRIMARY KEY AUTOINCREMENT,
    book_id   INTEGER NOT NULL REFERENCES books(id) ON DELETE CASCADE,
    label     TEXT    NOT NULL,
    terms     TEXT    NOT NULL DEFAULT '',
    monitored INTEGER NOT NULL DEFAULT 1,
    path      TEXT    NOT NULL DEFAULT '',
    format    TEXT    NOT NULL DEFAULT '',
    size      INTEGER NOT NULL DEFAULT 0,
    files     INTEGER NOT NULL DEFAULT 0,
    added_at  TEXT    NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX idx_book_audio_versions_book ON book_audio_versions(book_id);
