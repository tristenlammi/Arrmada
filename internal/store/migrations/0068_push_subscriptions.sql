-- Web Push subscriptions: one row per (user, browser/device). The endpoint is the
-- push service URL the browser handed us and is unique per subscription; p256dh and
-- auth are the client keys used to encrypt each payload. Rows are pruned when the
-- push service reports the subscription gone (404/410).
CREATE TABLE IF NOT EXISTS push_subscriptions (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  user_id    INTEGER NOT NULL,
  endpoint   TEXT NOT NULL UNIQUE,
  p256dh     TEXT NOT NULL,
  auth       TEXT NOT NULL,
  created_at TEXT NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX IF NOT EXISTS idx_push_subscriptions_user ON push_subscriptions(user_id);
