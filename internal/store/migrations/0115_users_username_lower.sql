-- 0115_users_username_lower: sign-in and account creation now compare names ignoring case
-- ('Mum@gmail.com' is 'mum@gmail.com'). This index keeps those lookups fast. It is
-- deliberately not UNIQUE: an instance that already has two accounts differing only by
-- case must still boot with both intact; the app logs them so the owner can choose.
CREATE INDEX IF NOT EXISTS idx_users_username_lower ON users(lower(username));
