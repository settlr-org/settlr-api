-- Attachments domain queries for sqlc with pgx/v5.
-- These replace manual Pool.Query/Exec calls in internal/attachments/handlers.go
-- Handler usage: Handler{Pool *pgxpool.Pool, Queries *db.Queries} where Queries wraps the pool.

-- name: GetExpenseGroupIDForAttachments :one
SELECT group_id FROM expenses WHERE id = $1 AND deleted_at IS NULL;

-- name: ListAttachments :many
SELECT id, user_id, file_url, file_name, mime_type, size_bytes, created_at
FROM expense_attachments
WHERE expense_id = $1
ORDER BY created_at DESC;

-- name: CreateAttachment :exec
INSERT INTO expense_attachments (id, expense_id, user_id, file_url, file_name, mime_type, size_bytes)
VALUES ($1, $2, $3, $4, $5, $6, $7);

-- name: GetAttachmentOwner :one
SELECT user_id, file_url FROM expense_attachments WHERE id = $1;

-- name: DeleteAttachment :exec
DELETE FROM expense_attachments WHERE id = $1;

-- name: GetAttachmentForServe :one
SELECT e.group_id, a.file_url, a.mime_type
FROM expense_attachments a
JOIN expenses e ON e.id = a.expense_id
WHERE a.id = $1 AND e.deleted_at IS NULL;

-- name: CheckAttachmentGroupMember :one
SELECT EXISTS (SELECT 1 FROM group_members WHERE group_id = $1 AND user_id = $2) AS is_member;
