-- 0059_convert_failures_any_item: quarantine TV episodes too, not just movies.
--
-- convert_failures was keyed on movie_id, so only movies could be blocklisted after repeated
-- failures. Episodes had no equivalent: an episode that always failed to encode (a bad rip, an
-- unsupported stream) was re-queued by every single sweep, forever. Now that the TV library is
-- convertible in bulk that's a real loop.
--
-- Re-key on an opaque item_key ("movie:123", "episode:76:2:5") so any convertible file can be
-- quarantined by the same counter.
CREATE TABLE IF NOT EXISTS convert_failures_v2 (
    item_key   TEXT PRIMARY KEY,
    count      INTEGER NOT NULL DEFAULT 0,
    last_error TEXT    NOT NULL DEFAULT '',
    updated_at TEXT    NOT NULL DEFAULT (datetime('now'))
);

-- Carry existing movie strikes over so nothing already quarantined silently un-quarantines.
INSERT OR IGNORE INTO convert_failures_v2 (item_key, count, last_error, updated_at)
SELECT 'movie:' || movie_id, count, last_error, updated_at FROM convert_failures;

DROP TABLE IF EXISTS convert_failures;
ALTER TABLE convert_failures_v2 RENAME TO convert_failures;
