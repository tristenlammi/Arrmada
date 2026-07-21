-- Convert's staged-replace journal. finalizeOutput records the swap it is about to
-- perform (part staged, original about to be retired) and deletes the row once the
-- rename + library re-tag succeed. On startup, any surviving row identifies a swap a
-- crash interrupted between "original retired" and "part renamed in": the converted
-- file is complete on disk as <final>.arrpart while the library record points at the
-- retired path — startup reconciliation completes the rename and repoints the record.
CREATE TABLE IF NOT EXISTS convert_swaps (
  part       TEXT PRIMARY KEY, -- the staged .arrpart path
  final      TEXT NOT NULL,    -- where it belongs
  src        TEXT NOT NULL,    -- the original (retired) library path
  kind       TEXT NOT NULL,    -- movie | episode
  movie_id   INTEGER NOT NULL DEFAULT 0,
  series_id  INTEGER NOT NULL DEFAULT 0,
  season     INTEGER NOT NULL DEFAULT 0,
  episode    INTEGER NOT NULL DEFAULT 0,
  created_at TEXT NOT NULL DEFAULT (datetime('now'))
);
