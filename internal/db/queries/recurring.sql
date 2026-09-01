-- Recurring domain queries for sqlc with pgx/v5.
-- These replace manual Pool.Query/Exec calls in internal/recurring/handlers.go
-- Handler usage: Handler{Pool *pgxpool.Pool, Queries *db.Queries} where Queries wraps the pool.

-- name: ListRecurringByGroup :many
SELECT id, description, amount, currency, split_mode, paid_by, frequency, next_run_at, last_run_at, active
FROM recurring_expenses
WHERE group_id = $1
ORDER BY created_at DESC;

-- name: CreateRecurringExpense :exec
INSERT INTO recurring_expenses (id, group_id, created_by, description, amount, currency, category_id, split_mode, splits, paid_by, frequency, next_run_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12);

-- name: CheckRecurringGroupMember :one
SELECT EXISTS(
    SELECT 1 FROM recurring_expenses re
    JOIN group_members gm ON gm.group_id = re.group_id AND gm.user_id = $2
    WHERE re.id = $1
) AS is_member;

-- name: UpdateRecurringActive :execrows
UPDATE recurring_expenses SET active = $3, updated_at = now()
WHERE id = $1 AND group_id IN (SELECT group_id FROM group_members WHERE user_id = $2);

-- name: DeleteRecurring :execrows
DELETE FROM recurring_expenses
WHERE id = $1 AND group_id IN (SELECT group_id FROM group_members WHERE user_id = $2);

-- name: ClaimDueRecurring :one
UPDATE recurring_expenses
SET next_run_at = CASE frequency
                    WHEN 'DAILY' THEN next_run_at + interval '1 day'
                    WHEN 'WEEKLY' THEN next_run_at + interval '7 days'
                    WHEN 'MONTHLY' THEN next_run_at + interval '1 month'
                    ELSE next_run_at + interval '1 year' END,
    last_run_at = next_run_at,
    updated_at = now()
WHERE id = (
    SELECT id FROM recurring_expenses
    WHERE active = true AND next_run_at <= now()
    ORDER BY next_run_at
    FOR UPDATE SKIP LOCKED
    LIMIT 1
)
RETURNING id, group_id, description, amount, currency, split_mode, category_id, splits, paid_by, next_run_at;

-- name: ListRecurringGroupMembers :many
SELECT user_id FROM group_members WHERE group_id = $1;

-- name: CreateRecurringExpenseInstance :exec
INSERT INTO expenses (id, group_id, description, amount, currency, paid_by, split_mode, category_id, expense_date, created_by)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9::date, $6);

-- name: CreateRecurringSplit :exec
INSERT INTO expense_splits (id, expense_id, user_id, amount) VALUES (gen_random_uuid(), $1, $2, $3);

-- name: CreateRecurringSplitWithPercentage :exec
INSERT INTO expense_splits (id, expense_id, user_id, amount, percentage) VALUES (gen_random_uuid(), $1, $2, $3, $4);

-- name: CreateRecurringSplitWithShares :exec
INSERT INTO expense_splits (id, expense_id, user_id, shares) VALUES (gen_random_uuid(), $1, $2, $3);

-- name: GetRecurringSplitsSum :one
SELECT coalesce(SUM(amount), 0)::bigint AS sum FROM expense_splits WHERE expense_id = $1;

-- name: TopUpRecurringSplit :exec
UPDATE expense_splits SET amount = amount + $3 WHERE expense_id = $1 AND user_id = $2;

-- name: CreateRecurringActivity :exec
INSERT INTO activity_events (group_id, actor_id, type, entity_type, entity_id, payload)
VALUES ($1, $2, 'EXPENSE_ADDED', 'expense', $3, json_build_object('description', $4::text, 'recurring', true));
