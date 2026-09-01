-- Expenses domain queries for sqlc with pgx/v5.
-- These replace manual Pool.Query/Exec calls in internal/expenses/handlers.go
-- Handler usage: Handler{Pool *pgxpool.Pool, Queries *db.Queries} where Queries wraps the pool.

-- name: GetGroupCurrency :one
SELECT currency FROM groups WHERE id = $1;

-- name: CheckIsGroupMember :one
SELECT EXISTS(SELECT 1 FROM group_members WHERE group_id = $1 AND user_id = $2) AS is_member;

-- name: IsExpenseMember :one
SELECT EXISTS(SELECT 1 FROM group_members WHERE group_id = $1 AND user_id = $2) AS is_member;

-- name: GetExpenseMemberRole :one
SELECT role FROM group_members WHERE group_id = $1 AND user_id = $2;

-- name: GetExpense :one
SELECT group_id, description, amount, currency, split_mode, paid_by, category_id, notes, expense_date, created_by, deleted_at FROM expenses WHERE id = $1;

-- name: GetExpenseForUpdate :one
SELECT group_id, created_by, deleted_at FROM expenses WHERE id = $1;

-- name: GetExpenseForDelete :one
SELECT group_id, created_by FROM expenses WHERE id = $1 AND deleted_at IS NULL;

-- name: CreateExpense :exec
INSERT INTO expenses (id, group_id, description, amount, currency, split_mode, paid_by, category_id, notes, expense_date, created_by, exchange_rate, base_currency, base_amount)
VALUES ($1, $2, $3, $4, $5, $6, $7, NULLIF($8::uuid, '00000000-0000-0000-0000-000000000000'::uuid), $9, $10, $11, $12, $13, $14);

-- name: InsertExpenseSplit :exec
INSERT INTO expense_splits (expense_id, user_id, amount, percentage, shares) VALUES ($1, $2, $3, $4, $5);

-- name: DeleteExpenseSplits :exec
DELETE FROM expense_splits WHERE expense_id = $1;

-- name: UpdateExpense :exec
UPDATE expenses SET description = $1, amount = $2, currency = $3, split_mode = $4, paid_by = $5, category_id = NULLIF($6::uuid, '00000000-0000-0000-0000-000000000000'::uuid), notes = $7, expense_date = $8, exchange_rate = $9, base_currency = $10, base_amount = $11, updated_at = now() WHERE id = $12;

-- name: SoftDeleteExpense :exec
UPDATE expenses SET deleted_at = now(), updated_at = now() WHERE id = $1;

-- name: ListExpenseSplits :many
SELECT user_id, amount, percentage, shares FROM expense_splits WHERE expense_id = $1;

-- name: ListExpenseSplitsByExpenseIDs :many
SELECT expense_id, user_id, amount, percentage, shares FROM expense_splits WHERE expense_id = ANY($1::uuid[]);

-- name: ListExpensesByGroup :many
SELECT e.id, e.description, e.amount, e.currency, e.split_mode, e.paid_by, e.category_id, e.notes, e.expense_date, e.created_by, e.created_at, e.updated_at
FROM expenses e WHERE e.group_id = $1 AND e.deleted_at IS NULL ORDER BY e.created_at DESC, e.id DESC LIMIT $2;

-- name: ListOtherGroupMembers :many
SELECT user_id FROM group_members WHERE group_id = $1 AND user_id != $2;

-- name: CreateNotificationExpenseAdded :exec
INSERT INTO notifications (user_id, type, title, body, data) VALUES ($1, 'EXPENSE_ADDED', $2, $3, $4);

-- name: InsertExpenseActivityAdded :exec
INSERT INTO activity_events (group_id, actor_id, type, entity_type, entity_id, payload) VALUES ($1, $2, 'EXPENSE_ADDED', 'expense', $3, $4);

-- name: InsertExpenseActivityUpdated :exec
INSERT INTO activity_events (group_id, actor_id, type, entity_type, entity_id, payload) VALUES ($1, $2, 'EXPENSE_UPDATED', 'expense', $3, $4);

-- name: InsertExpenseActivityDeleted :exec
INSERT INTO activity_events (group_id, actor_id, type, entity_type, entity_id, payload) VALUES ($1, $2, 'EXPENSE_DELETED', 'expense', $3, $4);

-- name: ListExpenses :many
SELECT e.id, e.description, e.amount, e.currency, e.split_mode, e.paid_by, e.category_id, e.notes, e.expense_date, e.created_by, e.created_at, e.updated_at
FROM expenses e WHERE e.group_id = $1 AND e.deleted_at IS NULL ORDER BY e.created_at DESC, e.id DESC;

-- name: GetExpenseDetails :one
SELECT id, group_id, description, amount, currency, split_mode, paid_by, category_id, notes, expense_date, created_by, created_at, updated_at, deleted_at, exchange_rate, base_currency, base_amount FROM expenses WHERE id = $1;

-- name: CountExpensesByGroup :one
SELECT COUNT(*) FROM expenses WHERE group_id = $1 AND deleted_at IS NULL;

-- name: ListExpensesWithCursor :many
SELECT e.id, e.description, e.amount, e.currency, e.split_mode, e.paid_by, e.category_id, e.notes, e.expense_date, e.created_by, e.created_at, e.updated_at
FROM expenses e WHERE e.group_id = $1 AND e.deleted_at IS NULL AND (e.created_at, e.id) < (SELECT ex.created_at, ex.id FROM expenses ex WHERE ex.id = $2) ORDER BY e.created_at DESC, e.id DESC LIMIT $3;
