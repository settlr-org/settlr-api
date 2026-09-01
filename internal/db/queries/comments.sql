-- Comments domain queries for sqlc with pgx/v5.
-- These replace manual Pool.Query/Exec calls in internal/comments/handlers.go
-- Handler usage: Handler{Pool *pgxpool.Pool, Queries *db.Queries} where Queries wraps the pool.

-- name: GetExpenseGroupIDForComments :one
SELECT group_id FROM expenses WHERE id = $1 AND deleted_at IS NULL;

-- name: ListComments :many
SELECT c.id, c.user_id, u.name, coalesce(u.avatar_url, '') AS avatar_url, c.body, c.created_at
FROM expense_comments c
JOIN users u ON u.id = c.user_id
WHERE c.expense_id = $1 AND c.deleted_at IS NULL
ORDER BY c.created_at ASC;

-- name: CreateComment :exec
INSERT INTO expense_comments (id, expense_id, user_id, body) VALUES ($1, $2, $3, $4);

-- name: CreateCommentActivity :exec
INSERT INTO activity_events (group_id, actor_id, type, entity_type, entity_id, payload)
VALUES ($1, $2, 'COMMENT_ADDED', 'comment', $3, $4);

-- name: ListCommentNotifyUsers :many
SELECT DISTINCT s.user_id FROM expense_splits s WHERE s.expense_id = $1 AND s.user_id != $2;

-- name: CreateMentionNotification :exec
INSERT INTO notifications (user_id, type, title, body, data) VALUES ($1, 'MENTION', $2, $3, $4);

-- name: GetCommentOwner :one
SELECT user_id FROM expense_comments WHERE id = $1 AND deleted_at IS NULL;

-- name: SoftDeleteComment :exec
UPDATE expense_comments SET deleted_at = now(), updated_at = now() WHERE id = $1;
