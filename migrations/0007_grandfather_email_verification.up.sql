-- Existing accounts were created before verification became mandatory. Treat
-- their prior successful use as the migration boundary; new registrations are
-- still created with email_verified_at = NULL.
UPDATE users
SET email_verified_at = COALESCE(created_at, now()), updated_at = now()
WHERE email_verified_at IS NULL;
