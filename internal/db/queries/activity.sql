-- Activity domain queries for sqlc with pgx/v5.
-- These replace manual Pool.Query/Exec calls in internal/activity/handlers.go
-- Handler usage: Handler{Pool *pgxpool.Pool, Queries *db.Queries} where Queries wraps the pool.

-- name: ListGlobalActivity :many
SELECT a.id, a.group_id, a.actor_id, a.type, a.entity_type, a.entity_id, a.payload, a.created_at
FROM activity_events a
WHERE a.group_id IN (SELECT group_id FROM group_members WHERE user_id = $1)
ORDER BY a.created_at DESC, a.id DESC
LIMIT $2;

-- name: ListGlobalActivityWithCursor :many
SELECT a.id, a.group_id, a.actor_id, a.type, a.entity_type, a.entity_id, a.payload, a.created_at
FROM activity_events a
WHERE a.group_id IN (SELECT group_id FROM group_members WHERE user_id = $1)
  AND (a.created_at, a.id) < (SELECT cursor.created_at, cursor.id FROM activity_events cursor WHERE cursor.id = $2)
ORDER BY a.created_at DESC, a.id DESC
LIMIT $3;
