-- 0147_indexer_caps: what each Torznab indexer says it supports.
--
-- A Test used to ask for the indexer's capabilities and throw the answer away, so an HTTP
-- 200 web page passed as "Connected" and nobody could see which searches an indexer takes.
-- caps_json is its last t=caps answer (indexer.Caps as JSON, '' = never read) and caps_at
-- when it was read; a passing Test, a Prowlarr sync and a weekly refresh fill them in.
ALTER TABLE indexers ADD COLUMN caps_json TEXT NOT NULL DEFAULT '';
ALTER TABLE indexers ADD COLUMN caps_at TIMESTAMP;
