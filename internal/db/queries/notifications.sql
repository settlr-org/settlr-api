-- Notifications domain queries for sqlc with pgx/v5.
-- These replace manual Pool.Query/Exec calls in internal/notifications/handlers.go
-- Handler usage: Handler{Pool *pgxpool.Pool, Queries *db.Queries} where Queries wraps the pool.

-- name: GetNotificationPreferences :one
SELECT email_enabled, push_enabled, friend_request_enabled, expense_enabled, settlement_enabled
FROM notification_preferences
WHERE user_id = $1;

-- name: UpsertNotificationPreferences :exec
INSERT INTO notification_preferences (user_id, email_enabled, push_enabled, friend_request_enabled, expense_enabled, settlement_enabled)
VALUES ($1, $2, $3, $4, $5, $6)
ON CONFLICT (user_id) DO UPDATE SET
    email_enabled = EXCLUDED.email_enabled,
    push_enabled = EXCLUDED.push_enabled,
    friend_request_enabled = EXCLUDED.friend_request_enabled,
    expense_enabled = EXCLUDED.expense_enabled,
    settlement_enabled = EXCLUDED.settlement_enabled;

-- name: ListNotifications :many
SELECT n.id, n.type, n.title, n.body, n.data, n.read_at, n.created_at
FROM notifications n
WHERE n.user_id = $1
ORDER BY n.created_at DESC, n.id DESC
LIMIT $2;

-- name: ListNotificationsWithCursor :many
SELECT n.id, n.type, n.title, n.body, n.data, n.read_at, n.created_at
FROM notifications n
WHERE n.user_id = $1
  AND (n.created_at, n.id) < (SELECT cursor.created_at, cursor.id FROM notifications cursor WHERE cursor.id = $2)
ORDER BY n.created_at DESC, n.id DESC
LIMIT $3;

-- name: CountUnreadNotifications :one
SELECT COUNT(*) FROM notifications WHERE user_id = $1 AND read_at IS NULL;

-- name: MarkNotificationRead :execrows
UPDATE notifications SET read_at = now() WHERE id = $1 AND user_id = $2 AND read_at IS NULL;

-- name: CheckNotificationExists :one
SELECT EXISTS (SELECT 1 FROM notifications WHERE id = $1 AND user_id = $2) AS exists;

-- name: MarkAllNotificationsRead :exec
UPDATE notifications SET read_at = now() WHERE user_id = $1 AND read_at IS NULL;
