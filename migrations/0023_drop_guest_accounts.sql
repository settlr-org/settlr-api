-- +goose Up
-- Server-side guest accounts (0022) were superseded by the on-device wallet: no account is needed for the wallet.
-- Remove any guest rows that were created while the endpoint existed, then the column.
DELETE FROM users WHERE is_guest;
DROP INDEX IF EXISTS idx_users_stale_guests;
ALTER TABLE users DROP COLUMN IF EXISTS is_guest;

-- +goose Down
ALTER TABLE users ADD COLUMN IF NOT EXISTS is_guest BOOLEAN NOT NULL DEFAULT FALSE;
