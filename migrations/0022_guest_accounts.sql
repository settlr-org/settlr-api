-- +goose Up
-- Guest accounts: created silently by the mobile app so the wallet works without signing in. A guest has a
-- placeholder e-mail, no password, and cannot use group features until it is upgraded to a real account.
ALTER TABLE users ADD COLUMN is_guest BOOLEAN NOT NULL DEFAULT FALSE;
CREATE INDEX idx_users_stale_guests ON users (created_at) WHERE is_guest;

-- +goose Down
DROP INDEX idx_users_stale_guests;
ALTER TABLE users DROP COLUMN is_guest;
