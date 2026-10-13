-- Personal domain queries for sqlc with pgx/v5.
-- Since the wallet (migration 0013) personal expenses are EXPENSE transactions;
-- /api/v1/personal/* is a compatibility layer over the transactions table
-- (description <-> title, notes <-> description, expense_date <-> occurred_at UTC date).
-- Balance adjustments are not personal expenses and are hidden here.

-- name: GetPersonalBudget :one
SELECT amount, currency FROM personal_budgets WHERE user_id = $1 AND month = $2;

-- name: UpsertPersonalBudget :exec
INSERT INTO personal_budgets(user_id, month, amount, currency) VALUES($1, $2, $3, $4)
ON CONFLICT(user_id, month) DO UPDATE SET amount = EXCLUDED.amount, currency = EXCLUDED.currency, updated_at = now();

-- name: ListPersonalExpenses :many
SELECT id, title AS description, amount, currency::text AS currency, category_id, description AS notes,
       (occurred_at AT TIME ZONE 'UTC')::date AS expense_date, created_at
FROM transactions
WHERE user_id = @user_id
  AND deleted_at IS NULL AND type = 'EXPENSE' AND occurred_at IS NOT NULL AND source <> 'ADJUSTMENT'
  AND (@q::text = '' OR title ILIKE '%' || @q::text || '%' OR description ILIKE '%' || @q::text || '%')
ORDER BY occurred_at DESC, created_at DESC
LIMIT @lim::int;

-- name: GetPersonalExpense :one
SELECT id, title AS description, amount, currency::text AS currency, category_id, description AS notes,
       (occurred_at AT TIME ZONE 'UTC')::date AS expense_date, created_at
FROM transactions
WHERE id = $1 AND user_id = $2
  AND deleted_at IS NULL AND type = 'EXPENSE' AND occurred_at IS NOT NULL AND source <> 'ADJUSTMENT';

-- name: SoftDeletePersonalExpense :exec
UPDATE transactions SET deleted_at = now(), updated_at = now()
WHERE id = $1 AND user_id = $2 AND deleted_at IS NULL AND type = 'EXPENSE' AND occurred_at IS NOT NULL AND source <> 'ADJUSTMENT';

-- name: GetPersonalExpenseTotal :one
SELECT COALESCE(SUM(amount), 0)::bigint AS total FROM transactions
WHERE user_id = $1 AND deleted_at IS NULL AND type = 'EXPENSE' AND occurred_at IS NOT NULL AND source <> 'ADJUSTMENT';

-- name: ListPersonalExpenseByCategory :many
SELECT COALESCE(c.name, 'Uncategorized')::text AS category, SUM(t.amount)::bigint AS total
FROM transactions t LEFT JOIN categories c ON c.id = t.category_id
WHERE t.user_id = $1 AND t.deleted_at IS NULL AND t.type = 'EXPENSE' AND t.occurred_at IS NOT NULL AND t.source <> 'ADJUSTMENT'
GROUP BY c.name
ORDER BY SUM(t.amount) DESC;

-- name: ListPersonalExpenseByMonth :many
SELECT to_char(occurred_at AT TIME ZONE 'UTC', 'YYYY-MM') AS month, SUM(amount)::bigint AS total
FROM transactions
WHERE user_id = $1 AND deleted_at IS NULL AND type = 'EXPENSE' AND source <> 'ADJUSTMENT'
  AND occurred_at >= (CURRENT_DATE - INTERVAL '6 months')
GROUP BY month
ORDER BY month;

-- name: ListPersonalExpensesExport :many
SELECT title AS description, amount, currency::text AS currency, (occurred_at AT TIME ZONE 'UTC')::date AS expense_date, description AS notes
FROM transactions
WHERE user_id = $1 AND deleted_at IS NULL AND type = 'EXPENSE' AND occurred_at IS NOT NULL AND source <> 'ADJUSTMENT'
ORDER BY occurred_at DESC;
