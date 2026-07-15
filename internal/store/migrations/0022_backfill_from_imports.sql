-- 0022_backfill_from_imports: correct source_release from the authoritative
-- import record. 0021 guessed from grab history, but when a movie had several
-- grabs the grab status can't say which one actually became the file (an import
-- flips ALL pending grabs to 'imported'), so it could pick the wrong release
-- (e.g. a 2160p grab for a 1080p file). The imports table maps the exact source
-- file to its library path, so it's authoritative. We take the source file's
-- basename (its release name) via the standard SQLite path-basename trick:
--   replace(p, rtrim(p, replace(p, '/', '')), '')
-- rtrim strips the trailing filename (all non-'/' chars) leaving the directory
-- prefix, which replace() then removes — yielding the basename.

UPDATE movies
SET source_release = (
    SELECT replace(i.source_path, rtrim(i.source_path, replace(i.source_path, '/', '')), '')
    FROM imports i WHERE i.target_path = movies.movie_file_path ORDER BY i.id DESC LIMIT 1
)
WHERE has_file = 1
  AND EXISTS (SELECT 1 FROM imports i WHERE i.target_path = movies.movie_file_path);

UPDATE movie_versions
SET source_release = (
    SELECT replace(i.source_path, rtrim(i.source_path, replace(i.source_path, '/', '')), '')
    FROM imports i WHERE i.target_path = movie_versions.file_path ORDER BY i.id DESC LIMIT 1
)
WHERE has_file = 1
  AND EXISTS (SELECT 1 FROM imports i WHERE i.target_path = movie_versions.file_path);
