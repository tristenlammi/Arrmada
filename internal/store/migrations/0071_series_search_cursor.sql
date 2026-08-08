-- 0071_series_search_cursor: let the missing-sweep reach every season, not just the first 12.
--
-- The sweep fans out one tvsearch per season with a gap, bounded by maxSeasonQueries (12)
-- and walked in ASCENDING order. On a show with more than twelve incomplete seasons, the
-- later ones were unreachable — the budget was spent on seasons 1..12 every single sweep,
-- forever. Bleach made it plain: "seasons=17 seasons_queried=12", so season 17 was never
-- searched and its missing episodes could only ever be grabbed by hand.
--
-- The anime absolute-number follow-up starves the same way: its remaining-episode list is
-- sorted ascending and capped at 3 queries, so it re-asked about the same three oldest gaps
-- every sweep and never advanced.
--
-- A cursor per series fixes both without raising the query budget: each sweep resumes where
-- the last one stopped and wraps around, so every gap is covered in turn.
ALTER TABLE series ADD COLUMN search_season_cursor INTEGER NOT NULL DEFAULT 0;
ALTER TABLE series ADD COLUMN search_abs_cursor INTEGER NOT NULL DEFAULT 0;
