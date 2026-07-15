-- 0021_backfill_source_release: files imported before source_release existed have
-- no stored release name, so they can't be considered for upgrades. Recover it
-- from the grab history — the most recent grab for that track that actually
-- imported (or is seeding) is the release the current file came from.

-- Default track (version_id 0 → the movie row).
UPDATE movies SET source_release = (
    SELECT title FROM grabs
    WHERE grabs.movie_id = movies.id AND grabs.version_id = 0
      AND grabs.status IN ('imported', 'seeded')
    ORDER BY grabs.id DESC LIMIT 1
)
WHERE has_file = 1 AND source_release = ''
  AND EXISTS (
    SELECT 1 FROM grabs
    WHERE grabs.movie_id = movies.id AND grabs.version_id = 0
      AND grabs.status IN ('imported', 'seeded')
  );

-- Extra version tracks.
UPDATE movie_versions SET source_release = (
    SELECT title FROM grabs
    WHERE grabs.movie_id = movie_versions.movie_id AND grabs.version_id = movie_versions.id
      AND grabs.status IN ('imported', 'seeded')
    ORDER BY grabs.id DESC LIMIT 1
)
WHERE has_file = 1 AND source_release = ''
  AND EXISTS (
    SELECT 1 FROM grabs
    WHERE grabs.movie_id = movie_versions.movie_id AND grabs.version_id = movie_versions.id
      AND grabs.status IN ('imported', 'seeded')
  );
