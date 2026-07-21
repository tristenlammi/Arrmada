-- 0066_insights_session_dedup: index the import dedupe key (user + item + start) so the
-- idempotency lookup (sessionExists) and cross-source dedupe are fast on large histories.
--
-- Deliberately NON-unique: two genuine same-second plays by one user on two devices are
-- legitimate live rows and must never be silently dropped. Import idempotency is enforced by
-- the sessionExists check plus a process-wide import serialization guard (see import.go),
-- not by a UNIQUE constraint that could discard real plays.
CREATE INDEX IF NOT EXISTS idx_stream_sessions_dedup
    ON stream_sessions(user_id, rating_key, started_at);
