-- 0185_user_notify_prefs: which of their notices each person wants pushed (Web Push and
-- their personal Apprise link): a request approved, declined, ready, and — for staff —
-- someone requesting something. A JSON object of event key → on/off; '' (everyone today)
-- and any key it doesn't name mean on, so nobody goes quiet by default and an event added
-- later delivers until someone turns it off. The in-app inbox always records everything.
ALTER TABLE users ADD COLUMN notify_prefs TEXT NOT NULL DEFAULT '';
