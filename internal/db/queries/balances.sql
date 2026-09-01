-- Balances domain queries for sqlc with pgx/v5.
-- These replace manual Pool.Query/Exec calls in internal/balances/handlers.go
-- Handler usage: Handler{Pool *pgxpool.Pool, Queries *db.Queries} where Queries wraps the pool.

-- name: GetUserDefaultCurrency :one
SELECT default_currency FROM users WHERE id = $1;

-- name: ListGroupMemberIDs :many
SELECT user_id FROM group_members WHERE group_id = $1;

-- name: SumPaidByGroup :many
SELECT paid_by AS user_id, SUM(ROUND(amount * COALESCE(exchange_rate, 1))::bigint)::bigint AS total
FROM expenses
WHERE group_id = $1 AND deleted_at IS NULL
GROUP BY paid_by;

-- name: SumOwedByGroup :many
SELECT s.user_id, SUM(ROUND(s.amount * COALESCE(e.exchange_rate, 1))::bigint)::bigint AS total
FROM expense_splits s
JOIN expenses e ON e.id = s.expense_id
WHERE e.group_id = $1 AND e.deleted_at IS NULL
GROUP BY s.user_id;

-- name: ListSettlementsByGroup :many
SELECT from_user, to_user, amount FROM settlements WHERE group_id = $1 AND deleted_at IS NULL;

-- name: GetMyGroupBalances :many
SELECT g.id, g.name, g.currency,
       COALESCE(paid.total, 0)::bigint - COALESCE(owed.total, 0)::bigint + COALESCE(recv.total, 0)::bigint - COALESCE(sent.total, 0)::bigint AS balance
FROM groups g
JOIN group_members gm ON gm.group_id = g.id AND gm.user_id = $1
LEFT JOIN (
    SELECT group_id, paid_by, SUM(ROUND(amount * COALESCE(exchange_rate, 1))::bigint)::bigint AS total
    FROM expenses WHERE deleted_at IS NULL GROUP BY group_id, paid_by
) paid ON paid.group_id = g.id AND paid.paid_by = $1
LEFT JOIN (
    SELECT e.group_id, s.user_id, SUM(ROUND(s.amount * COALESCE(e.exchange_rate, 1))::bigint)::bigint AS total
    FROM expense_splits s JOIN expenses e ON e.id = s.expense_id
    WHERE e.deleted_at IS NULL GROUP BY e.group_id, s.user_id
) owed ON owed.group_id = g.id AND owed.user_id = $1
LEFT JOIN (
    SELECT group_id, from_user AS user_id, SUM(amount)::bigint AS total FROM settlements WHERE deleted_at IS NULL GROUP BY group_id, from_user
) recv ON recv.group_id = g.id AND recv.user_id = $1
LEFT JOIN (
    SELECT group_id, to_user AS user_id, SUM(amount)::bigint AS total FROM settlements WHERE deleted_at IS NULL GROUP BY group_id, to_user
) sent ON sent.group_id = g.id AND sent.user_id = $1
WHERE g.archived_at IS NULL;
