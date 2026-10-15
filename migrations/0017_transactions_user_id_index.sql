-- +goose Up
-- Keyset paging of the account export (WHERE user_id = $1 AND id > $2 ORDER BY id).
CREATE INDEX IF NOT EXISTS idx_transactions_user_id ON transactions (user_id, id) WHERE deleted_at IS NULL;

-- +goose Down
DROP INDEX IF EXISTS idx_transactions_user_id;
