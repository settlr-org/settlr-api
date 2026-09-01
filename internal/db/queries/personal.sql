-- Personal domain queries for sqlc with pgx/v5.
-- These replace manual Pool.Query/Exec calls in internal/personal/handlers.go
-- Handler usage: Handler{Pool *pgxpool.Pool, Queries *db.Queries} where Queries wraps the pool.
-- Transaction usage: qtx := h.Queries.WithTx(tx)

-- name: GetPersonalBudget :one
SELECT amount, currency FROM personal_budgets WHERE user_id = $1 AND month = $2;

-- name: UpsertPersonalBudget :exec
INSERT INTO personal_budgets(user_id, month, amount, currency) VALUES($1, $2, $3, $4)
ON CONFLICT(user_id, month) DO UPDATE SET amount = EXCLUDED.amount, currency = EXCLUDED.currency, updated_at = now();

-- name: ListPersonalExpenses :many
SELECT id, description, amount, currency, category_id, notes, expense_date, base_currency, exchange_rate, base_amount, created_at
FROM personal_expenses
WHERE user_id = $1
  AND deleted_at IS NULL
  AND ($2::text = '' OR description ILIKE '%' || $2 || '%' OR notes ILIKE '%' || $2 || '%')
ORDER BY expense_date DESC, created_at DESC
LIMIT $3;

-- name: CreatePersonalExpense :exec
INSERT INTO personal_expenses (id, user_id, description, amount, currency, category_id, notes, expense_date, base_currency, exchange_rate, base_amount)
VALUES ($1, $2, $3, $4, $5, NULLIF($6::uuid, '00000000-0000-0000-0000-000000000000'::uuid), $7, $8, $9, $10, $11);

-- name: GetPersonalExpense :one
SELECT description, amount, currency, category_id, notes, expense_date, created_at, base_currency, exchange_rate, base_amount
FROM personal_expenses
WHERE id = $1 AND user_id = $2 AND deleted_at IS NULL;

-- name: CheckPersonalExpenseExists :one
SELECT EXISTS(SELECT 1 FROM personal_expenses WHERE id = $1 AND user_id = $2 AND deleted_at IS NULL) AS exists;

-- name: UpdatePersonalExpenseDescription :exec
UPDATE personal_expenses SET description = $1, updated_at = now() WHERE id = $2;

-- name: UpdatePersonalExpenseAmount :exec
UPDATE personal_expenses SET amount = $1, updated_at = now() WHERE id = $2;

-- name: UpdatePersonalExpenseCurrency :exec
UPDATE personal_expenses SET currency = $1, updated_at = now() WHERE id = $2;

-- name: UpdatePersonalExpenseCategoryClear :exec
UPDATE personal_expenses SET category_id = NULL, updated_at = now() WHERE id = $1;

-- name: UpdatePersonalExpenseCategory :exec
UPDATE personal_expenses SET category_id = $1, updated_at = now() WHERE id = $2;

-- name: UpdatePersonalExpenseNotes :exec
UPDATE personal_expenses SET notes = $1, updated_at = now() WHERE id = $2;

-- name: UpdatePersonalExpenseDate :exec
UPDATE personal_expenses SET expense_date = $1, updated_at = now() WHERE id = $2;

-- name: SoftDeletePersonalExpense :exec
UPDATE personal_expenses SET deleted_at = now(), updated_at = now() WHERE id = $1 AND user_id = $2;

-- name: GetPersonalExpenseTotal :one
SELECT COALESCE(SUM(amount), 0)::bigint AS total FROM personal_expenses WHERE user_id = $1 AND deleted_at IS NULL;

-- name: ListPersonalExpenseByCategory :many
SELECT COALESCE(c.name, 'Uncategorized') AS category, SUM(pe.amount)::bigint AS total
FROM personal_expenses pe LEFT JOIN categories c ON c.id = pe.category_id
WHERE pe.user_id = $1 AND pe.deleted_at IS NULL
GROUP BY c.name
ORDER BY SUM(pe.amount) DESC;

-- name: ListPersonalExpenseByMonth :many
SELECT to_char(expense_date, 'YYYY-MM') AS month, SUM(amount)::bigint AS total
FROM personal_expenses
WHERE user_id = $1 AND deleted_at IS NULL AND expense_date >= CURRENT_DATE - INTERVAL '6 months'
GROUP BY month
ORDER BY month;

-- name: ListPersonalExpensesExport :many
SELECT description, amount, currency, expense_date, notes
FROM personal_expenses
WHERE user_id = $1 AND deleted_at IS NULL
ORDER BY expense_date DESC;
