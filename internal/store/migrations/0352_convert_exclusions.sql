-- 0352_convert_exclusions: titles you told Convert to leave alone ("Don't convert this").
-- A film, a whole show (season -1) or one season of a show. Files of a title listed here
-- are never picked, listed as worth converting, or counted in the space still to save. The
-- title is kept as it was named when it was left out, for the list in Settings.
CREATE TABLE IF NOT EXISTS convert_exclusions (
    kind       TEXT    NOT NULL,             -- 'movie' | 'series'
    title_id   INTEGER NOT NULL,             -- the movie's or the show's id
    season     INTEGER NOT NULL DEFAULT -1,  -- one season of a show; -1 = the whole title
    title      TEXT    NOT NULL DEFAULT '',
    created_at TEXT    NOT NULL DEFAULT (datetime('now')),
    PRIMARY KEY (kind, title_id, season)
);
