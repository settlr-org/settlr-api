-- +goose Up
-- Reports, trends and ledger filters select a user's entries by date: the ledger view's sort
-- time for wallet transactions is COALESCE(occurred_at, due_at). A month of a 100k-row
-- history drops from a full scan (~200 ms per aggregate) to an index range scan (~10 ms).
CREATE INDEX IF NOT EXISTS idx_transactions_user_sort_at
    ON transactions (user_id, (COALESCE(occurred_at, due_at))) WHERE deleted_at IS NULL;

-- +goose Down
DROP INDEX IF EXISTS idx_transactions_user_sort_at;
