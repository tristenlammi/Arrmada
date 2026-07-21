-- Observed stall time per buffer spell. Event COUNTS under 5s sampling are noisy
-- (short stalls are missed; client quirks over-report); duration — wall-clock
-- covered by consecutive buffering polls — is the honest number the Reliability
-- view now leads with. Existing rows default to 0 (count-only legacy events).
ALTER TABLE buffer_events ADD COLUMN duration_ms INTEGER NOT NULL DEFAULT 0;
