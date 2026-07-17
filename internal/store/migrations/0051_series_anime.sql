-- 0051_series_anime: anime support. series_type drives episode numbering
-- ("standard" | "anime"); episodes.absolute_number is the 1..N position across the
-- whole run (specials excluded) used to match absolutely-numbered anime releases.

ALTER TABLE series ADD COLUMN series_type TEXT NOT NULL DEFAULT 'standard';
ALTER TABLE episodes ADD COLUMN absolute_number INTEGER NOT NULL DEFAULT 0;
CREATE INDEX IF NOT EXISTS idx_episodes_absolute ON episodes(series_id, absolute_number);
