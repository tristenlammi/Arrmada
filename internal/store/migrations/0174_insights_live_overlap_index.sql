-- 0174_insights_live_overlap_index: index the live-recorded plays by user and start, for
-- the Tautulli import's "was this play already recorded live?" check and the repair that
-- removes past double-counts.
--
-- Partial (session_key <> '') because only live rows are ever compared against: an empty
-- session_key marks a row that came from an import (see 0072). The check asks for one
-- user's live rows that started shortly before an imported play's window, which this
-- index answers without walking the user's whole history once per imported row.
CREATE INDEX IF NOT EXISTS idx_stream_sessions_live_user
    ON stream_sessions(user_id, started_at) WHERE session_key <> '';
