-- 0172_request_quotas: optional per-user request limits.
--
-- request_usage is the ledger a quota is counted from: one row per new ask that counts
-- (a movie, a book, or a series request's seasons), written when the request is made or
-- a declined one asked for again, and deleted (or reduced) when it's withdrawn, declined
-- or trimmed on approval — so a refund is immediate. Following someone else's request
-- writes nothing. A requester joining an already-approved book request with the other
-- format writes one book row under their own name: that starts a new download nobody
-- approved, so it counts.
--
-- users.quota_* override the global limits (Settings → Users → Request limits) for one
-- person: -1 follows the global limit, 0 is unlimited, n is n per window. Every limit
-- starts unlimited, so nothing changes on upgrade until the owner sets one.
CREATE TABLE request_usage (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id    INTEGER NOT NULL,
    request_id INTEGER NOT NULL,
    kind       TEXT    NOT NULL CHECK (kind IN ('movie', 'season', 'book')),
    units      INTEGER NOT NULL,
    created_at INTEGER NOT NULL
);
CREATE INDEX idx_request_usage_user ON request_usage(user_id, created_at);
CREATE INDEX idx_request_usage_request ON request_usage(request_id);

ALTER TABLE users ADD COLUMN quota_movies INTEGER NOT NULL DEFAULT -1;
ALTER TABLE users ADD COLUMN quota_seasons INTEGER NOT NULL DEFAULT -1;
ALTER TABLE users ADD COLUMN quota_books INTEGER NOT NULL DEFAULT -1;
