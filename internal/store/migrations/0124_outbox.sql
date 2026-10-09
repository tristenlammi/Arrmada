-- 0124_outbox: side effects of an import that must happen, kept until they have.
--
-- Reindexing Convert and Subtitles, the requester's "ready" message and the audiobook
-- catalogue refresh used to ride the event bus, which drops events when a subscriber is
-- busy and forgets everything on a restart. Now the code that records an import writes
-- one row here per consumer (in the same transaction where it can), and a dispatcher
-- runs each row's handler until it succeeds, backing off between attempts.
--
-- Times are unix seconds; 0 means "not yet". A row is due while done_at and failed_at are
-- both 0 and next_at has passed. gen counts how many times the row was enqueued again
-- while still pending (a newer import of the same thing): the dispatcher only marks the
-- row done when gen hasn't moved since it read it, so a change that arrives while the
-- handler runs is never swallowed by the earlier run.
CREATE TABLE outbox (
    id         INTEGER PRIMARY KEY,
    topic      TEXT    NOT NULL,
    consumer   TEXT    NOT NULL,
    payload    TEXT    NOT NULL,
    dedupe_key TEXT    NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL,
    attempts   INTEGER NOT NULL DEFAULT 0,
    next_at    INTEGER NOT NULL DEFAULT 0,
    done_at    INTEGER NOT NULL DEFAULT 0,
    failed_at  INTEGER NOT NULL DEFAULT 0,
    last_error TEXT    NOT NULL DEFAULT '',
    gen        INTEGER NOT NULL DEFAULT 0
);

CREATE INDEX idx_outbox_due ON outbox (done_at, failed_at, next_at);

-- One pending row per consumer and key: a second enqueue of the same thing updates the
-- waiting row instead of adding another. Finished and failed rows don't count, so the
-- same key can be queued again once the earlier row is settled.
CREATE UNIQUE INDEX idx_outbox_pending_dedupe ON outbox (consumer, dedupe_key)
    WHERE dedupe_key != '' AND done_at = 0 AND failed_at = 0;
