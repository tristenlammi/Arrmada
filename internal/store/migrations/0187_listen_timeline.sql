-- 0187_listen_timeline: one timeline per audiobook place, so nothing an app sends is
-- lost without a trace and every place can be put back.
--
-- listen_history.kind says what each row is:
--   applied    the place moved (the reason column keeps the existing words)
--   before     the place just before a non-routine change (a rewind, a restore, a finish…)
--   rejected   a place an app sent that wasn't used (older than the saved one, or a jump
--              that wasn't proven) — offered back to the person as a "later spot"
--   held       a big jump waiting for proof
--   discarded  an app removed the place (DELETE from the app is now a soft delete)
--   restored   the person put a place back
-- dismissed marks a rejected row the person said no to, so it isn't offered again.
-- Rows made before this read as applied, which is what they were.
ALTER TABLE listen_history ADD COLUMN kind TEXT NOT NULL DEFAULT 'applied';
ALTER TABLE listen_history ADD COLUMN dismissed INTEGER NOT NULL DEFAULT 0;

-- When an app removed the place (unix ms; 0 = not removed). A removed place is hidden
-- from apps and kept for 90 days under "Recently removed"; a new report starts fresh.
ALTER TABLE listen_progress ADD COLUMN discarded_at INTEGER NOT NULL DEFAULT 0;
