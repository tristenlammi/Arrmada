-- 0065_request_subscribers: multiple users can be interested in the same request.
-- The first user is the requester (on the requests row); anyone who requests the
-- same title afterwards becomes a subscriber and shares the notifications
-- (approved / declined / ready). Also used on re-request of a declined title:
-- the previous requester is kept as a subscriber so they still hear the outcome.
CREATE TABLE IF NOT EXISTS request_subscribers (
    request_id INTEGER NOT NULL,
    user_id    INTEGER NOT NULL,
    user_name  TEXT    NOT NULL DEFAULT '',
    created_at TEXT    NOT NULL DEFAULT (datetime('now')),
    UNIQUE(request_id, user_id)
);
