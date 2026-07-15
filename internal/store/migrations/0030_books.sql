-- 0030_books: the Books module (Readarr replacement). Books are keyed by their Open
-- Library work id (a string, not a TMDB int). "Quality" for books is FORMAT preference
-- (EPUB > AZW3 > MOBI > PDF), stored in the shared quality_profiles table's
-- format_scores; a dedicated book ranker reads it (the resolution ladder is ignored).

CREATE TABLE IF NOT EXISTS books (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    ol_key          TEXT NOT NULL UNIQUE,
    title           TEXT NOT NULL,
    author          TEXT NOT NULL DEFAULT '',
    year            INTEGER NOT NULL DEFAULT 0,
    cover_url       TEXT NOT NULL DEFAULT '',
    description     TEXT NOT NULL DEFAULT '',
    subjects_json   TEXT NOT NULL DEFAULT '',
    monitored       INTEGER NOT NULL DEFAULT 1,
    quality_profile TEXT NOT NULL DEFAULT '',
    has_file        INTEGER NOT NULL DEFAULT 0,
    file_path       TEXT NOT NULL DEFAULT '',
    format          TEXT NOT NULL DEFAULT '',
    size_bytes      INTEGER NOT NULL DEFAULT 0,
    added_at        TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_books_added ON books(added_at DESC, id DESC);

-- Seed the starter book quality profiles. format_scores encodes the format preference
-- (higher = preferred); size_cap_gb 0 (ebooks are tiny).
INSERT INTO quality_profiles (media_type, name, base, allowed_resolutions, size_cap_gb, small_bias, format_scores)
VALUES
    ('book', 'Ebook (EPUB preferred)', '', '[]', 0, 0, '{"EPUB":40,"AZW3":30,"MOBI":20,"PDF":10}'),
    ('book', 'Any format',             '', '[]', 0, 0, '{"EPUB":15,"AZW3":14,"MOBI":13,"PDF":12,"CBZ":8,"FB2":8}');
