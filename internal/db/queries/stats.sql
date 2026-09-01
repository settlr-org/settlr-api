-- Stats domain queries for sqlc with pgx/v5.
-- These replace manual Pool.Query/Exec calls in internal/stats/handlers.go
-- Handler usage: Handler{Pool *pgxpool.Pool, Queries *db.Queries} where Queries wraps the pool.

-- name: GetGroupStatsTotal :one
SELECT coalesce(SUM(amount * COALESCE(exchange_rate, 1)), 0)::bigint AS total,
       coalesce(ROUND(AVG(amount * COALESCE(exchange_rate, 1))), 0)::bigint AS avg,
       COUNT(*)::bigint AS count
FROM expenses
WHERE group_id = $1 AND deleted_at IS NULL;

-- name: GetGroupStatsTotalThisMonth :one
SELECT coalesce(SUM(amount * COALESCE(exchange_rate, 1)), 0)::bigint AS total,
       coalesce(ROUND(AVG(amount * COALESCE(exchange_rate, 1))), 0)::bigint AS avg,
       COUNT(*)::bigint AS count
FROM expenses
WHERE group_id = $1 AND deleted_at IS NULL AND expense_date >= date_trunc('month', CURRENT_DATE);

-- name: GetGroupStatsTotalLast30 :one
SELECT coalesce(SUM(amount * COALESCE(exchange_rate, 1)), 0)::bigint AS total,
       coalesce(ROUND(AVG(amount * COALESCE(exchange_rate, 1))), 0)::bigint AS avg,
       COUNT(*)::bigint AS count
FROM expenses
WHERE group_id = $1 AND deleted_at IS NULL AND expense_date >= CURRENT_DATE - interval '30 days';

-- name: GetGroupStatsTotalThisYear :one
SELECT coalesce(SUM(amount * COALESCE(exchange_rate, 1)), 0)::bigint AS total,
       coalesce(ROUND(AVG(amount * COALESCE(exchange_rate, 1))), 0)::bigint AS avg,
       COUNT(*)::bigint AS count
FROM expenses
WHERE group_id = $1 AND deleted_at IS NULL AND expense_date >= date_trunc('year', CURRENT_DATE);

-- name: GetStatsByCategory :many
SELECT c.name, coalesce(c.icon, 'tag') AS icon, coalesce(SUM(ROUND(e.amount * COALESCE(e.exchange_rate, 1))::bigint), 0)::bigint AS total, COUNT(*)::bigint AS count
FROM expenses e
LEFT JOIN categories c ON c.id = e.category_id
WHERE e.group_id = $1 AND e.deleted_at IS NULL
GROUP BY c.name, c.icon
ORDER BY total DESC;

-- name: GetStatsByCategoryThisMonth :many
SELECT c.name, coalesce(c.icon, 'tag') AS icon, coalesce(SUM(ROUND(e.amount * COALESCE(e.exchange_rate, 1))::bigint), 0)::bigint AS total, COUNT(*)::bigint AS count
FROM expenses e
LEFT JOIN categories c ON c.id = e.category_id
WHERE e.group_id = $1 AND e.deleted_at IS NULL AND e.expense_date >= date_trunc('month', CURRENT_DATE)
GROUP BY c.name, c.icon
ORDER BY total DESC;

-- name: GetStatsByCategoryLast30 :many
SELECT c.name, coalesce(c.icon, 'tag') AS icon, coalesce(SUM(ROUND(e.amount * COALESCE(e.exchange_rate, 1))::bigint), 0)::bigint AS total, COUNT(*)::bigint AS count
FROM expenses e
LEFT JOIN categories c ON c.id = e.category_id
WHERE e.group_id = $1 AND e.deleted_at IS NULL AND e.expense_date >= CURRENT_DATE - interval '30 days'
GROUP BY c.name, c.icon
ORDER BY total DESC;

-- name: GetStatsByCategoryThisYear :many
SELECT c.name, coalesce(c.icon, 'tag') AS icon, coalesce(SUM(ROUND(e.amount * COALESCE(e.exchange_rate, 1))::bigint), 0)::bigint AS total, COUNT(*)::bigint AS count
FROM expenses e
LEFT JOIN categories c ON c.id = e.category_id
WHERE e.group_id = $1 AND e.deleted_at IS NULL AND e.expense_date >= date_trunc('year', CURRENT_DATE)
GROUP BY c.name, c.icon
ORDER BY total DESC;

-- name: GetStatsByMember :many
SELECT u.id, u.name, coalesce(SUM(ROUND(e.amount * COALESCE(e.exchange_rate, 1))::bigint), 0)::bigint AS total, COUNT(e.id)::bigint AS count
FROM users u
LEFT JOIN expenses e ON e.paid_by = u.id AND e.group_id = $1 AND e.deleted_at IS NULL
WHERE u.id IN (SELECT user_id FROM group_members WHERE group_id = $1)
GROUP BY u.id, u.name;

-- name: GetStatsMonthly :many
SELECT to_char(expense_date, 'YYYY-MM') AS month, SUM(ROUND(amount * COALESCE(exchange_rate, 1))::bigint)::bigint AS total
FROM expenses
WHERE group_id = $1 AND deleted_at IS NULL
GROUP BY month
ORDER BY month;

-- name: GetStatsMonthlyThisMonth :many
SELECT to_char(expense_date, 'YYYY-MM') AS month, SUM(ROUND(amount * COALESCE(exchange_rate, 1))::bigint)::bigint AS total
FROM expenses
WHERE group_id = $1 AND deleted_at IS NULL AND expense_date >= date_trunc('month', CURRENT_DATE)
GROUP BY month
ORDER BY month;

-- name: GetStatsMonthlyLast30 :many
SELECT to_char(expense_date, 'YYYY-MM') AS month, SUM(ROUND(amount * COALESCE(exchange_rate, 1))::bigint)::bigint AS total
FROM expenses
WHERE group_id = $1 AND deleted_at IS NULL AND expense_date >= CURRENT_DATE - interval '30 days'
GROUP BY month
ORDER BY month;

-- name: GetStatsMonthlyThisYear :many
SELECT to_char(expense_date, 'YYYY-MM') AS month, SUM(ROUND(amount * COALESCE(exchange_rate, 1))::bigint)::bigint AS total
FROM expenses
WHERE group_id = $1 AND deleted_at IS NULL AND expense_date >= date_trunc('year', CURRENT_DATE)
GROUP BY month
ORDER BY month;
