-- Search domain queries for sqlc with pgx/v5.
-- These replace manual Pool.Query/Exec calls in internal/search/handlers.go
-- Handler usage: Handler{Pool *pgxpool.Pool, Queries *db.Queries} where Queries wraps the pool.

-- name: SearchUsers :many
SELECT id, name, coalesce(avatar_url, '') AS avatar_url
FROM users
WHERE is_anonymous = false
  AND (name ILIKE '%' || $1 || '%' OR email ILIKE '%' || $1 || '%')
LIMIT 10;

-- name: SearchGroups :many
SELECT g.id, g.name
FROM groups g
JOIN group_members gm ON gm.group_id = g.id
WHERE gm.user_id = $1
  AND g.archived_at IS NULL
  AND g.name ILIKE '%' || $2 || '%'
LIMIT 10;

-- name: SearchExpenses :many
SELECT e.id, e.group_id, e.description, e.amount, e.currency
FROM expenses e
WHERE e.deleted_at IS NULL
  AND e.group_id IN (SELECT group_id FROM group_members WHERE user_id = $1)
  AND (e.description ILIKE '%' || $2 || '%' OR e.notes ILIKE '%' || $2 || '%')
ORDER BY e.created_at DESC
LIMIT 10;
