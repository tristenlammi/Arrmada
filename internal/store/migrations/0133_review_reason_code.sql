-- 0133_review_reason_code: why a download is held, as a code the Review page acts on.
--
-- The Review queue mixes five different problems — content that doesn't match, a download
-- tied to nothing, filenames whose episode numbering can't be read, an import that keeps
-- failing, and a download with nothing importable in it — and offered the same four
-- buttons for all of them, so most actions on most cards could only fail. Each review now
-- carries its reason as a code. Older rows are classified from their reason text, in this
-- order; anything unrecognised is 'mismatch', which offers today's actions.
ALTER TABLE import_reviews ADD COLUMN reason_code TEXT NOT NULL DEFAULT '';

UPDATE import_reviews SET reason_code = 'import_failed'
 WHERE reason_code = '' AND reason LIKE 'Import failed %';
UPDATE import_reviews SET reason_code = 'unmatched'
 WHERE reason_code = '' AND expected_id = 0;
UPDATE import_reviews SET reason_code = 'numbering'
 WHERE reason_code = '' AND reason LIKE '%could be matched to an episode%';
UPDATE import_reviews SET reason_code = 'no_media'
 WHERE reason_code = '' AND (reason LIKE '%no ebook or audiobook%' OR reason LIKE '%no audio%');
UPDATE import_reviews SET reason_code = 'mismatch'
 WHERE reason_code = '';
