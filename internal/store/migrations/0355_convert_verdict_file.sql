-- 0355_convert_verdict_file: Convert's verdicts are about a FILE, not about a title.
--
-- A permanent skip ("didn't come out smaller", "couldn't reach the quality bar") and the
-- three-failure block were keyed by the title alone, so they outlived the file they were
-- about: a film marked "not smaller" as a 6 GB WEB-DL was never looked at again after the
-- upgrader brought in a 60 GB remux. Each row now records the file it was about (its path
-- and size, as the library index has them), and a row only holds while the index still has
-- that file; a different file is judged afresh.
--
-- Existing rows are given the file the index has for them now. That is the file they were
-- most likely about, and the conservative reading: they keep holding exactly as before
-- until that file changes. A row whose title has no indexed file keeps an empty path,
-- which means "any file" — the old behaviour — until the next verdict replaces it.
-- A "reverted" skip stays title-wide on purpose: the revert itself swaps the file back, and
-- the runner must not convert the original again on its own.
ALTER TABLE convert_skips ADD COLUMN file_path TEXT NOT NULL DEFAULT '';
ALTER TABLE convert_skips ADD COLUMN file_size INTEGER NOT NULL DEFAULT 0;
-- The kind of encoder a "not smaller" or "quality" verdict came from ('' = not recorded,
-- or not about an encoder). A GPU's verdict only holds while the GPU is in use: the CPU
-- makes smaller files at the same quality, so it gets its own chance.
ALTER TABLE convert_skips ADD COLUMN encoder TEXT NOT NULL DEFAULT '';

ALTER TABLE convert_failures ADD COLUMN file_path TEXT NOT NULL DEFAULT '';
ALTER TABLE convert_failures ADD COLUMN file_size INTEGER NOT NULL DEFAULT 0;

-- Test-encode measurements record the encoder too. Rows from before are left with '' and
-- no longer count for any encoder: which one made them isn't known, and a GPU's figure
-- trusted for the CPU is exactly what kept files from being converted. The Library shows
-- the estimate for them until the next test encode measures them again.
ALTER TABLE convert_measured ADD COLUMN encoder TEXT NOT NULL DEFAULT '';

CREATE TEMP TABLE convert_0355_keys AS
SELECT CASE
         WHEN media_type = 'episode' THEN 'episode:' || series_id || ':' || season || ':' || episode
         WHEN version_id > 0 THEN 'movie:' || movie_id || ':v:' || version_id
         ELSE 'movie:' || movie_id
       END AS item_key,
       path, size_bytes
  FROM convert_library;
CREATE INDEX temp.convert_0355_keys_item ON convert_0355_keys(item_key);

UPDATE convert_skips
   SET file_path = (SELECT k.path FROM convert_0355_keys k WHERE k.item_key = convert_skips.item_key LIMIT 1),
       file_size = (SELECT k.size_bytes FROM convert_0355_keys k WHERE k.item_key = convert_skips.item_key LIMIT 1)
 WHERE kind <> 'reverted'
   AND EXISTS (SELECT 1 FROM convert_0355_keys k WHERE k.item_key = convert_skips.item_key);

UPDATE convert_failures
   SET file_path = (SELECT k.path FROM convert_0355_keys k WHERE k.item_key = convert_failures.item_key LIMIT 1),
       file_size = (SELECT k.size_bytes FROM convert_0355_keys k WHERE k.item_key = convert_failures.item_key LIMIT 1)
 WHERE EXISTS (SELECT 1 FROM convert_0355_keys k WHERE k.item_key = convert_failures.item_key);

DROP TABLE convert_0355_keys;
