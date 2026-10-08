-- 0097_convert_skip_attempts: how many times in a row a file hit the same temporary skip.
-- A full scratch or library disk used to be retried on the very next pick, so one stuck
-- file hot-looped the runner. Each repeat of the same kind now waits longer (1 h, 6 h,
-- 24 h); a different kind, a success or "Try again" starts the count over.
ALTER TABLE convert_skips ADD COLUMN attempts INTEGER NOT NULL DEFAULT 0;
