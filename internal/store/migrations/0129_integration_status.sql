-- 0129_integration_status: how each integration (an indexer, a download client,
-- FlareSolverr) has been answering, kept so a failing one can be shown, paused and
-- explained after a restart.
--
-- Before this, an indexer failure lived only in one search's error map and a log line:
-- nothing could show a dead indexer on its row, and an expired login was retried on
-- every search of every sweep.
--
-- kind is "indexer", "download_client" or "flaresolverr"; ref is the integration's id
-- within its kind (an indexer's row id as text, "default" for FlareSolverr). Times are
-- unix seconds; 0 means "never". last_error is redacted before it is stored (no API key,
-- passkey, token or URL query string) and capped in length. backoff_until is when an
-- integration that keeps failing will next be tried by background work; a person's own
-- search always tries it.
CREATE TABLE integration_status (
    kind                 TEXT    NOT NULL,
    ref                  TEXT    NOT NULL,
    last_ok_at           INTEGER NOT NULL DEFAULT 0,
    last_error_at        INTEGER NOT NULL DEFAULT 0,
    last_error           TEXT    NOT NULL DEFAULT '',
    consecutive_failures INTEGER NOT NULL DEFAULT 0,
    failing_since        INTEGER NOT NULL DEFAULT 0,
    backoff_until        INTEGER NOT NULL DEFAULT 0,
    avg_ms               INTEGER NOT NULL DEFAULT 0,
    updated_at           INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (kind, ref)
);

-- Queries and failures per integration per hour ("2026-10-09T14", UTC), for the
-- "214 searches, 3 failed (24h)" line. Rows older than 48 hours are pruned.
CREATE TABLE integration_counts (
    kind     TEXT    NOT NULL,
    ref      TEXT    NOT NULL,
    hour     TEXT    NOT NULL,
    queries  INTEGER NOT NULL DEFAULT 0,
    failures INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (kind, ref, hour)
);
