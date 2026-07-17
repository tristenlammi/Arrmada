-- 0037_convert_rule_steps: Rules v2 (R4) — branching flows. A rule's body becomes a tree of
-- steps (action | condition-with-then/else, arbitrarily nested) so a flow can branch, e.g.
-- "if resolution is 4K → quality 22, else → quality 24". Stored as JSON; the linear actions
-- from R2 remain valid (a flat action list).

ALTER TABLE convert_rules ADD COLUMN steps_json TEXT NOT NULL DEFAULT '';
