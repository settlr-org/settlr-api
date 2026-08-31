DROP TABLE IF EXISTS oauth_identities;
-- This migration cannot safely make password_hash required again while OAuth-only
-- accounts exist, so it intentionally leaves the column nullable on rollback.
