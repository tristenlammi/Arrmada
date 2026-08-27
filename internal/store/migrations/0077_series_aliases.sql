-- 0077_series_aliases: alternate release titles for a series, optionally pinned to one
-- TMDB season.
--
-- Anime arcs are routinely released as if they were a separate show. Bleach's
-- Thousand-Year Blood War ships as "BLEACH Thousand-Year Blood War S02E02" and
-- "[SubsPlease] Bleach - Sennen Kessen Hen - 15", while TMDB folds the whole arc into
-- Bleach season 17 numbered continuously. Nothing matched those titles, so every such
-- release was discarded before it was ever scored.
--
-- tmdb_season pins where the alias' numbering lands:
--   0  = title-only. The release is recognised as this series and numbered normally.
--   >0 = the alias' episode numbers are read INSIDE that season, so "S02E02" means
--        "second cour, second episode" of that season rather than the series' own S2.
--
-- Deliberately per-series and user-created: a series with no rows here behaves exactly
-- as it did before, and alias numbering only ever applies to a release that matched an
-- alias — a release matching the series' real title is untouched.
CREATE TABLE series_aliases (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    series_id   INTEGER NOT NULL REFERENCES series(id) ON DELETE CASCADE,
    title       TEXT NOT NULL,
    -- title_key is the normalized form (parser.TitleKey) actually used for matching;
    -- title is kept verbatim so the UI can show what the user typed and searches can
    -- use it as a query.
    title_key   TEXT NOT NULL,
    tmdb_season INTEGER NOT NULL DEFAULT 0,
    created_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(series_id, title_key)
);

CREATE INDEX idx_series_aliases_series ON series_aliases(series_id);
CREATE INDEX idx_series_aliases_key ON series_aliases(title_key);
