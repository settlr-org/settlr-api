-- Categories domain queries for sqlc with pgx/v5.
-- These replace manual Pool.Query/Exec calls in internal/categories/handlers.go
-- Handler usage: Handler{Pool *pgxpool.Pool, Queries *db.Queries} where Queries wraps the pool.

-- name: ListCategories :many
SELECT id, name, icon, color, is_system, coalesce(grouping, 'Uncategorized') AS grouping
FROM categories
WHERE is_system = true OR owner_id = $1
ORDER BY grouping, is_system DESC, name;

-- name: CreateCategory :exec
INSERT INTO categories (id, name, icon, color, grouping, is_system, owner_id)
VALUES ($1, $2, $3, $4, $5, false, $6);
