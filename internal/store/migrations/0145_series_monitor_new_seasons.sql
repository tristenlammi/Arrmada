-- 0145_series_monitor_new_seasons: whether a season new to a show is monitored when a
-- refresh adds it.
--
-- A refresh used to give new seasons and episodes the series flag, whatever the owner had
-- chosen for the show's seasons, and pausing then resuming a show re-monitored every season
-- they'd switched off. The series flag is now only a pause gate; this says what a newly
-- announced season gets.
--
-- Existing shows start with their series flag, so library-scanned shows (added unmonitored)
-- don't start monitoring new seasons on their own.
ALTER TABLE series ADD COLUMN monitor_new_seasons INTEGER NOT NULL DEFAULT 1;
UPDATE series SET monitor_new_seasons = monitored;
