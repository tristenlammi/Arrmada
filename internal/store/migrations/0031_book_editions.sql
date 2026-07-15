-- 0031_book_editions: a book can have BOTH an ebook and an audiobook edition (they're
-- separate downloads/formats). Track each independently; has_file means "has either".
-- Which editions a book wants is derived from its quality profile's format_scores
-- (ebook formats present → wants ebook; audiobook formats present → wants audiobook).

ALTER TABLE books ADD COLUMN ebook_path      TEXT NOT NULL DEFAULT '';
ALTER TABLE books ADD COLUMN ebook_format    TEXT NOT NULL DEFAULT '';
ALTER TABLE books ADD COLUMN ebook_size      INTEGER NOT NULL DEFAULT 0;
ALTER TABLE books ADD COLUMN audiobook_path   TEXT NOT NULL DEFAULT '';
ALTER TABLE books ADD COLUMN audiobook_format TEXT NOT NULL DEFAULT '';
ALTER TABLE books ADD COLUMN audiobook_size   INTEGER NOT NULL DEFAULT 0;

-- Existing single-file books were ebooks — move them into the ebook slot.
UPDATE books SET ebook_path = file_path, ebook_format = format, ebook_size = size_bytes WHERE has_file = 1;

-- Add profiles covering the audiobook editions (the EPUB-only one from 0030 stays).
INSERT INTO quality_profiles (media_type, name, base, allowed_resolutions, size_cap_gb, small_bias, format_scores)
VALUES
    ('book', 'Ebook + Audiobook', '', '[]', 0, 0, '{"EPUB":40,"AZW3":30,"MOBI":20,"PDF":10,"M4B":40,"MP3":25,"M4A":20}'),
    ('book', 'Audiobook only',    '', '[]', 0, 0, '{"M4B":40,"MP3":30,"M4A":25,"FLAC":15}');
