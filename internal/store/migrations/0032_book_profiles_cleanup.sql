-- 0032_book_profiles_cleanup: books don't need the movie-style quality editor. Ship
-- exactly three fixed presets — Ebook, Audiobook, Ebook + Audiobook — that just encode
-- which edition(s) to grab. Consolidate the four seeded from 0030/0031 down to these.

-- Rename the ebook-only and audiobook-only presets to clean names.
UPDATE quality_profiles SET name = 'Ebook'
  WHERE media_type = 'book' AND name = 'Ebook (EPUB preferred)';
UPDATE quality_profiles SET name = 'Audiobook'
  WHERE media_type = 'book' AND name = 'Audiobook only';

-- Move any book off the redundant "Any format" preset onto "Ebook" before removing it.
UPDATE books SET quality_profile =
    'custom:' || (SELECT id FROM quality_profiles WHERE media_type = 'book' AND name = 'Ebook' LIMIT 1)
  WHERE quality_profile =
    'custom:' || (SELECT id FROM quality_profiles WHERE media_type = 'book' AND name = 'Any format' LIMIT 1);

DELETE FROM quality_profiles WHERE media_type = 'book' AND name = 'Any format';
