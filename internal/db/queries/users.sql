-- Users domain queries for sqlc with pgx/v5.
-- These replace manual Pool.Query/Exec calls in internal/users/handlers.go
-- Handler usage: Handler{Pool *pgxpool.Pool, Queries *db.Queries} where Queries wraps the pool.

-- name: GetUserPaymentInfo :one
SELECT coalesce(bank_qr_url, '') AS bank_qr_url, coalesce(bank_name, '') AS bank_name, coalesce(payment_handle, '') AS payment_handle
FROM users WHERE id = $1;

-- name: UpdateUserPaymentInfo :exec
UPDATE users SET bank_qr_url = $1, bank_name = $2, payment_handle = $3, updated_at = now() WHERE id = $4;

-- name: CheckFriendshipAccepted :one
SELECT EXISTS(
    SELECT 1 FROM friendships
    WHERE ((user_id = $1 AND friend_id = $2) OR (user_id = $2 AND friend_id = $1))
      AND status = 'ACCEPTED'
) AS is_friend;

-- name: GetUserPaymentInfoForFriend :one
SELECT coalesce(bank_qr_url, '') AS bank_qr_url, coalesce(bank_name, '') AS bank_name, coalesce(payment_handle, '') AS payment_handle
FROM users WHERE id = $1 AND is_anonymous = false;

-- name: GetMe :one
SELECT name, email, coalesce(avatar_url, '') AS avatar_url, default_currency, timezone, email_verified_at, password_hash IS NOT NULL AS has_password
FROM users WHERE id = $1;

-- name: UpdateUserName :exec
UPDATE users SET name = $1, updated_at = now() WHERE id = $2;

-- name: UpdateUserEmail :exec
UPDATE users SET email = $1, updated_at = now(), email_verified_at = NULL WHERE id = $2;

-- name: UpdateUserAvatar :exec
UPDATE users SET avatar_url = $1, updated_at = now() WHERE id = $2;

-- name: UpdateUserDefaultCurrency :exec
UPDATE users SET default_currency = $1, updated_at = now() WHERE id = $2;

-- name: UpdateUserTimezone :exec
UPDATE users SET timezone = $1, updated_at = now() WHERE id = $2;

-- name: GetUserPasswordHash :one
SELECT password_hash FROM users WHERE id = $1;

-- name: UpdateUserPasswordHash :exec
UPDATE users SET password_hash = $1, updated_at = now() WHERE id = $2;

-- name: SoftDeleteUser :exec
UPDATE users SET name = 'Deleted User', email = $1, password_hash = 'deleted', avatar_url = NULL, is_anonymous = true, updated_at = now() WHERE id = $2;

-- name: SearchUsersByName :many
SELECT u.id, u.name, u.email, coalesce(u.avatar_url, '') AS avatar_url,
       EXISTS (
           SELECT 1 FROM friendships f
           WHERE f.user_id = LEAST($2::uuid, u.id) AND f.friend_id = GREATEST($2::uuid, u.id)
             AND f.status = 'PENDING' AND f.action_by = $2
       ) AS requested
FROM users u
WHERE u.id <> $2
  AND u.is_anonymous = false
  AND (u.name ILIKE '%' || $1 || '%' OR u.email ILIKE '%' || $1 || '%')
ORDER BY u.name
LIMIT 20;

-- name: GetUserPublic :one
SELECT name, coalesce(avatar_url, '') AS avatar_url FROM users WHERE id = $1 AND is_anonymous = false;

-- name: GetUserByIDForMe :one
SELECT name, email, coalesce(avatar_url, '') AS avatar_url, default_currency, timezone, email_verified_at::text AS email_verified_at_text, password_hash IS NOT NULL AS has_password FROM users WHERE id = $1;
