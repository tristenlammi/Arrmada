-- 0048_convert_logs: persist the Convert activity console so its history survives an app
-- restart / update instead of living only in memory. Trimmed to the most recent 5000 lines by
-- the writer. Read oldest-first for the UI console.

CREATE TABLE IF NOT EXISTS convert_logs (
    id    INTEGER PRIMARY KEY AUTOINCREMENT,
    at    INTEGER NOT NULL, -- unix seconds
    level TEXT    NOT NULL, -- "info" | "warn" | "error"
    msg   TEXT    NOT NULL
);
