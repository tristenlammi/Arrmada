-- 0144_series_numbering_pending: a renumber a refresh found but didn't apply.
--
-- When another source numbers a show differently (anime moving onto TVDB's per-season
-- listing), refreshes never move files on their own; they only noted it in History, and
-- the owner had to press Refresh blind to accept it. The proposal is stored here instead,
-- with every file it would move, so the series page can show the remap and offer Apply
-- and Dismiss.
--
-- plan_hash identifies the plan (Apply refuses a plan that changed since it was shown);
-- '' means nothing is pending. dismissed_hash is the plan the owner turned down, so the
-- same plan isn't proposed again while a different one still is.
CREATE TABLE series_numbering_pending (
    series_id      INTEGER PRIMARY KEY REFERENCES series(id) ON DELETE CASCADE,
    from_source    TEXT NOT NULL DEFAULT '',
    to_source      TEXT NOT NULL DEFAULT '',
    plan_hash      TEXT NOT NULL DEFAULT '',
    remaps_json    TEXT NOT NULL DEFAULT '[]',
    dismissed_hash TEXT NOT NULL DEFAULT '',
    created_at     TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
