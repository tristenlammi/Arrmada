-- 0181_user_plex_link: linking a Plex account to any account, not only to the ones Plex
-- sign-in creates.
--
-- plex_username is the Plex name the link was made with, so the account menu and
-- Settings → Users can say "Linked as <name>". Rows linked before this migration fill it
-- in on their next Plex sign-in.
--
-- password_login says whether the account has a password anyone knows. Accounts made by
-- Plex sign-in (and the Overseerr import) get a random one nobody knows, so Plex is their
-- only way in: unlinking Plex from such an account would lock its owner out, and the next
-- Plex sign-in would make them a new, empty account. Existing Plex-linked rows are
-- assumed to be those until an admin sets their password.
ALTER TABLE users ADD COLUMN plex_username TEXT NOT NULL DEFAULT '';
ALTER TABLE users ADD COLUMN password_login INTEGER NOT NULL DEFAULT 1;
UPDATE users SET password_login = 0 WHERE plex_id IS NOT NULL;
