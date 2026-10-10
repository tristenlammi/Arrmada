-- 0186_session_activity: what each browser session is, so people can see where they're
-- signed in and end one they don't recognise (Me → Account).
--
-- last_seen_at is touched at most every ten minutes while the session is used. user_agent
-- is the browser's own description (cut short), shown as "Chrome on Windows". network is
-- deliberately coarse — "local" for the home network, else the address with its last part
-- dropped (203.0.113.x, an IPv6 /48) — and only ever shown to the session's own user.
-- Sessions made before this read as "unknown device", last seen when they were made.
ALTER TABLE sessions ADD COLUMN last_seen_at TIMESTAMP;
ALTER TABLE sessions ADD COLUMN user_agent TEXT NOT NULL DEFAULT '';
ALTER TABLE sessions ADD COLUMN network TEXT NOT NULL DEFAULT '';
