-- 0029_user_auto_approve: auto-approval is a per-user property (not a global setting).
-- When a user with auto_approve=1 requests a title from Discover, it's approved and
-- acquired immediately; otherwise it waits in the request queue for an admin/manager.

ALTER TABLE users ADD COLUMN auto_approve INTEGER NOT NULL DEFAULT 0;

-- Existing admins auto-approve their own requests by default.
UPDATE users SET auto_approve = 1 WHERE role = 'admin';
