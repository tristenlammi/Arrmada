-- 0162_notifications_config: settings for alert connections that aren't an Apprise URL.
--
-- The first is Web Push ("This device"): kind 'webpush', no URL, and config
-- {"user_id": N} — the admin whose subscribed browsers and phones get the alerts.
-- Other connection kinds keep '{}'.
ALTER TABLE notifications ADD COLUMN config TEXT NOT NULL DEFAULT '{}';
