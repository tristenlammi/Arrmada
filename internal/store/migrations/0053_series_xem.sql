-- 0053_series_xem: TheXEM scene mapping for anime. Store the show's TVDB id (the XEM
-- key) and cache the fetched scene→absolute map so the resolver can translate a
-- split-season release (e.g. "Dragon Ball Super S02E01") onto TMDB's continuous order.

ALTER TABLE series ADD COLUMN tvdb_id INTEGER NOT NULL DEFAULT 0;

CREATE TABLE IF NOT EXISTS series_scene_map (
    series_id  INTEGER PRIMARY KEY,
    scene_json TEXT    NOT NULL DEFAULT '', -- {"2-1":15,"2-2":16,...} scene "S-E" -> absolute
    fetched_at INTEGER NOT NULL DEFAULT 0
);
