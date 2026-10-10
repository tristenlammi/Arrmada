-- 0171_auto_approve_types: auto-approve per media type.
--
-- One auto_approve flag covered everything, so a Plex user the owner shares with had every
-- series request (every season of a long show) approved and searched at once. Each type
-- now has its own flag; every existing account keeps exactly what it had (all three set
-- from the old flag). The old column stays for a rolled-back build and is kept in step
-- (on only when all three are), but nothing reads it any more.
--
-- The Plex sign-in default moves to a list of types (plex_login_auto_approve_types),
-- worked out in Go when it isn't saved yet: an owner who explicitly turned the old toggle
-- on keeps every type, one who turned it off keeps none, and an install that never touched
-- it gives new sign-ins movies only.
ALTER TABLE users ADD COLUMN auto_approve_movie INTEGER NOT NULL DEFAULT 0;
ALTER TABLE users ADD COLUMN auto_approve_series INTEGER NOT NULL DEFAULT 0;
ALTER TABLE users ADD COLUMN auto_approve_book INTEGER NOT NULL DEFAULT 0;
UPDATE users SET auto_approve_movie = auto_approve, auto_approve_series = auto_approve, auto_approve_book = auto_approve;
