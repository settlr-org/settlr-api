-- Export domain queries for sqlc with pgx/v5.
-- These replace manual Pool.Query/Exec calls in internal/export/handlers.go
-- Handler usage: Handler{Pool *pgxpool.Pool, Queries *db.Queries} where Queries wraps the pool.

-- name: ExportMyCSV :many
SELECT e.expense_date, g.name AS group_name, e.description, coalesce(c.name, '') AS category,
       pu.name AS paid_by, e.amount, e.currency, e.split_mode,
       su.name AS split_user, coalesce(s.amount, 0)::bigint AS split_amount
FROM expenses e
JOIN groups g ON g.id = e.group_id
JOIN group_members gm ON gm.group_id = e.group_id AND gm.user_id = $1
JOIN users pu ON pu.id = e.paid_by
LEFT JOIN categories c ON c.id = e.category_id
LEFT JOIN expense_splits s ON s.expense_id = e.id
LEFT JOIN users su ON su.id = s.user_id
WHERE e.deleted_at IS NULL
ORDER BY e.expense_date, e.created_at;

-- name: ExportMyJSON :many
SELECT e.id, e.group_id, g.name AS group_name, e.description, coalesce(c.name, '') AS category, pu.name AS paid_by, e.amount, e.currency, e.split_mode, e.expense_date, e.created_at
FROM expenses e
JOIN groups g ON g.id = e.group_id
JOIN group_members gm ON gm.group_id = e.group_id AND gm.user_id = $1
JOIN users pu ON pu.id = e.paid_by
LEFT JOIN categories c ON c.id = e.category_id
WHERE e.deleted_at IS NULL
ORDER BY e.expense_date, e.created_at;

-- name: ExportGroupJSON :many
SELECT e.id, e.description, coalesce(c.name, '') AS category, pu.name AS paid_by, e.amount, e.currency, e.split_mode, e.expense_date, e.created_at, e.notes
FROM expenses e
JOIN users pu ON pu.id = e.paid_by
LEFT JOIN categories c ON c.id = e.category_id
WHERE e.group_id = $1 AND e.deleted_at IS NULL
ORDER BY e.expense_date, e.created_at;

-- name: ExportGroupCSV :many
SELECT e.expense_date, e.description, coalesce(c.name, '') AS category,
       pu.name AS paid_by, e.amount, e.currency, e.split_mode,
       su.name AS split_user, coalesce(s.amount, 0)::bigint AS split_amount
FROM expenses e
JOIN users pu ON pu.id = e.paid_by
LEFT JOIN categories c ON c.id = e.category_id
LEFT JOIN expense_splits s ON s.expense_id = e.id
LEFT JOIN users su ON su.id = s.user_id
WHERE e.group_id = $1 AND e.deleted_at IS NULL
ORDER BY e.expense_date, e.created_at;

-- name: ExportGroupSettlements :many
SELECT st.settled_at, f.name AS from_name, t.name AS to_name, st.amount, st.currency, st.note
FROM settlements st
JOIN users f ON f.id = st.from_user
JOIN users t ON t.id = st.to_user
WHERE st.group_id = $1 AND st.deleted_at IS NULL
ORDER BY st.settled_at;

-- name: CheckExportGroupMember :one
SELECT EXISTS(SELECT 1 FROM group_members WHERE group_id = $1 AND user_id = $2) AS is_member;

-- name: GetExportGroupName :one
SELECT name FROM groups WHERE id = $1;
