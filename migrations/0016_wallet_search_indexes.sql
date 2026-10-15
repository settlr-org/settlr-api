-- +goose Up
-- Global search (GET /search) matches wallet transactions by title/description with
-- ILIKE '%q%'. pg_trgm is already enabled (0005); these GIN indexes let the planner
-- skip a per-user scan on large histories. Tags, accounts, loans, budgets and plans
-- are small per user and rely on their existing per-user indexes.
CREATE EXTENSION IF NOT EXISTS pg_trgm;
CREATE INDEX IF NOT EXISTS idx_transactions_title_trgm ON transactions USING gin (title gin_trgm_ops) WHERE deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_transactions_description_trgm ON transactions USING gin (description gin_trgm_ops) WHERE deleted_at IS NULL;

-- +goose Down
DROP INDEX IF EXISTS idx_transactions_title_trgm;
DROP INDEX IF EXISTS idx_transactions_description_trgm;
