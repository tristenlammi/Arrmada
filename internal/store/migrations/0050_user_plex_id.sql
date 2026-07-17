-- 0050_user_plex_id: link a user to a Plex account for "Sign in with Plex". Nullable (password
-- accounts have none); unique among the rows that have one.

ALTER TABLE users ADD COLUMN plex_id TEXT;
CREATE UNIQUE INDEX IF NOT EXISTS idx_users_plex_id ON users(plex_id) WHERE plex_id IS NOT NULL;
