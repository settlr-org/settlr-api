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
VALUES ($1, $2, $3, $4, $5, $6, NULLIF($7::uuid, '00000000-0000-0000-0000-000000000000'::uuid), $8, $9, $10, $11, $12);

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
-- Claims one due schedule and moves it past now() in a single step: run_at is the latest
-- occurrence <= now() (the instance to create); missed earlier periods are skipped rather
-- than back-filled. $1 lists schedules that already failed in this run so a broken row
-- cannot be re-claimed in a hot loop.
WITH due AS (
    SELECT id, next_run_at,
           CASE frequency
               WHEN 'DAILY' THEN interval '1 day'
               WHEN 'WEEKLY' THEN interval '7 days'
               WHEN 'MONTHLY' THEN interval '1 month'
               ELSE interval '1 year' END AS step
    FROM recurring_expenses
    WHERE active = true AND next_run_at <= now() AND NOT (id = ANY(coalesce($1::uuid[], '{}')))
    ORDER BY next_run_at
    FOR UPDATE SKIP LOCKED
    LIMIT 1
), occ AS (
    SELECT d.id, d.step, max(t)::timestamptz AS run_at
    FROM due d, generate_series(d.next_run_at, now(), d.step) AS t
    GROUP BY d.id, d.step
)
UPDATE recurring_expenses r
SET last_run_at = occ.run_at,
    next_run_at = occ.run_at + occ.step,
    updated_at = now()
FROM occ
WHERE r.id = occ.id
RETURNING r.id, r.group_id, r.description, r.amount, r.currency, r.split_mode, r.category_id, r.splits, r.paid_by, r.next_run_at, occ.run_at;

-- name: ListRecurringGroupMembers :many
SELECT user_id FROM group_members WHERE group_id = $1;

-- name: CreateRecurringExpenseInstance :exec
INSERT INTO expenses (id, group_id, description, amount, currency, paid_by, split_mode, category_id, expense_date, created_by)
VALUES ($1, $2, $3, $4, $5, $6, $7, NULLIF($8::uuid, '00000000-0000-0000-0000-000000000000'::uuid), $9::date, $6);

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
