-- 0057_series_scene_override: a manual, authoritative scene-season → TMDB mapping.
--
-- Anime is released under broadcast "seasons" (cours) that frequently don't line up with
-- TMDB's numbering: Dandadan ships as S01 + S02 while TMDB has one 24-episode season, so
-- "DAN.DA.DAN.S02E01" is really S01E13. The resolver already tries per-cour positioning,
-- TheXEM, and an air-date-gap heuristic — but XEM doesn't carry every show (Dragon Ball
-- Super isn't in it) and a heuristic is a guess. When they're all wrong there was no way
-- for a user to correct it short of importing every episode by hand.
--
-- One row says "scene season N starts at TMDB SxxEyy"; the rest of the cour follows by
-- offset. Kept in its own table, NOT in series_scene_map, because that one is the cached
-- XEM fetch and gets overwritten — this is user intent and must survive a refresh.
CREATE TABLE IF NOT EXISTS series_scene_overrides (
    series_id    INTEGER NOT NULL REFERENCES series(id) ON DELETE CASCADE,
    scene_season INTEGER NOT NULL, -- the season number releases use
    tmdb_season  INTEGER NOT NULL, -- the TMDB season its first episode lives in
    tmdb_episode INTEGER NOT NULL, -- the TMDB episode that scene E01 maps to
    PRIMARY KEY (series_id, scene_season)
);
