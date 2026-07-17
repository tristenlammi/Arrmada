-- 0040_convert_rule_schedule: per-rule schedule. Each Convert rule can now say *when* it runs on
-- the nightly sweep — an optional "HH:MM"–"HH:MM" window (empty = any time, may wrap past midnight)
-- — instead of everything sharing one global window.

ALTER TABLE convert_rules ADD COLUMN window_start TEXT NOT NULL DEFAULT '';
ALTER TABLE convert_rules ADD COLUMN window_end   TEXT NOT NULL DEFAULT '';
