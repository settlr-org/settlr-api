-- Categories domain queries for sqlc with pgx/v5.
-- These replace manual Pool.Query/Exec calls in internal/categories/handlers.go
-- Handler usage: Handler{Pool *pgxpool.Pool, Queries *db.Queries} where Queries wraps the pool.

-- name: ListCategories :many
-- Categories the user reordered (POST /categories/reorder) come first in their order.
SELECT c.id, c.name, c.icon, c.color, c.is_system, coalesce(c.grouping, 'Uncategorized') AS grouping,
       co.sort_order AS sort_order
FROM categories c
LEFT JOIN category_order co ON co.category_id = c.id AND co.user_id = $1
WHERE c.is_system = true OR c.owner_id = $1
ORDER BY co.sort_order IS NULL, co.sort_order, grouping, c.is_system DESC, c.name;

-- name: CreateCategory :exec
INSERT INTO categories (id, name, icon, color, grouping, is_system, owner_id)
VALUES ($1, $2, $3, $4, $5, false, $6);

-- name: GetVisibleCategory :one
SELECT id, name, icon, color, is_system, owner_id, coalesce(grouping, 'Uncategorized') AS grouping
FROM categories
WHERE id = @id AND (is_system OR owner_id = @user_id);

-- name: UpdateCategory :one
UPDATE categories SET name = @name, icon = @icon, color = @color, grouping = @grouping
WHERE id = @id AND owner_id = @user_id AND NOT is_system
RETURNING id, name, icon, color, is_system, coalesce(grouping, 'Uncategorized') AS grouping;

-- name: DeleteCategory :execrows
-- Transactions, personal and group expenses keep existing with category_id set to NULL (FK ON DELETE SET NULL).
DELETE FROM categories WHERE id = @id AND owner_id = @user_id AND NOT is_system;

-- name: CountVisibleCategories :one
SELECT COUNT(*)::int FROM categories WHERE id = ANY(@ids::uuid[]) AND (is_system OR owner_id = @user_id::uuid);

-- name: UpsertCategoryOrder :exec
INSERT INTO category_order (user_id, category_id, sort_order) VALUES ($1, $2, $3)
ON CONFLICT (user_id, category_id) DO UPDATE SET sort_order = EXCLUDED.sort_order;
