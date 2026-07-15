-- 0036_convert_rule_flows: Rules v2 (R2). A rule is no longer a fixed set of columns but a
-- structured filters + actions list (a linear flow), stored as JSON. The legacy columns from
-- 0035 stay for back-compat but are unused going forward. See CONVERT-RULES-V2.md.

ALTER TABLE convert_rules ADD COLUMN filters_json TEXT NOT NULL DEFAULT '';
ALTER TABLE convert_rules ADD COLUMN actions_json TEXT NOT NULL DEFAULT '';
