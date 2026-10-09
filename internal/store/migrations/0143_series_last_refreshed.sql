-- 0143_series_last_refreshed: when a show's metadata was last pulled successfully.
--
-- The scheduled refresh skipped every show stored as ended, forever, and a refresh never
-- rewrote the stored status anyway — so a revived show never gained its new season, and a
-- show that ended never read as ended. Ended shows are now re-checked once a week, and this
-- is how the sweep knows which are due. '' means never (every show before this upgrade).
ALTER TABLE series ADD COLUMN last_refreshed_at TEXT NOT NULL DEFAULT '';
