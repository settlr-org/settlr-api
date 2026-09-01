-- Friends domain queries for sqlc with pgx/v5.
-- These replace manual Pool.Query/Exec calls in internal/friends/handlers.go and internal/friends/direct.go
-- Handler usage: Handler{Pool *pgxpool.Pool, Queries *db.Queries, Mailer *mailer.Mailer} where Queries wraps the pool.
-- Transaction usage: qtx := h.Queries.WithTx(tx)

-- name: GetFriendshipStatus :one
SELECT status FROM friendships WHERE user_id = $1 AND friend_id = $2;

-- name: GetFriendship :one
SELECT id, user_id, friend_id, status, action_by, created_at, updated_at FROM friendships WHERE user_id = $1 AND friend_id = $2;

-- name: ListFriends :many
SELECT
    f.id AS friendship_id,
    f.status,
    (CASE WHEN f.user_id = $1 THEN f.friend_id ELSE f.user_id END)::uuid AS other_id,
    u.name,
    coalesce(u.avatar_url, '') AS avatar_url
FROM friendships f
JOIN users u ON u.id = CASE WHEN f.user_id = $1 THEN f.friend_id ELSE f.user_id END
WHERE (f.user_id = $1 OR f.friend_id = $1) AND f.status = 'ACCEPTED';

-- name: ListFriendRequests :many
SELECT
    f.id AS friendship_id,
    f.action_by AS from_user,
    u.name,
    coalesce(u.avatar_url, '') AS avatar_url,
    f.created_at
FROM friendships f
JOIN users u ON u.id = f.action_by
WHERE (f.user_id = $1 OR f.friend_id = $1) AND f.status = 'PENDING' AND f.action_by != $1
ORDER BY f.created_at DESC;

-- name: CheckUserExistsNotAnonymous :one
SELECT EXISTS(SELECT 1 FROM users WHERE id = $1 AND is_anonymous = false) AS exists;

-- name: GetUserNameByID :one
SELECT name FROM users WHERE id = $1;

-- name: GetUserLowerEmailByID :one
SELECT lower(email) AS lower_email FROM users WHERE id = $1;

-- name: GetFriendUserByEmail :one
SELECT id FROM users WHERE lower(email) = lower($1);

-- name: GetFriendUserIDByEmail :one
SELECT id FROM users WHERE lower(email) = lower($1) LIMIT 1;

-- name: SendFriendRequest :exec
INSERT INTO friendships (id, user_id, friend_id, status, action_by) VALUES ($1, $2, $3, 'PENDING', $4);

-- name: AcceptFriendRequest :execrows
UPDATE friendships SET status = 'ACCEPTED', updated_at = now() WHERE user_id = $1 AND friend_id = $2 AND status = 'PENDING' AND action_by != $3;

-- name: AcceptFriendRequestReturning :one
UPDATE friendships SET status = 'ACCEPTED', updated_at = now() WHERE user_id = $1 AND friend_id = $2 AND status = 'PENDING' AND action_by != $3 RETURNING id;

-- name: RejectFriendRequest :execrows
DELETE FROM friendships WHERE user_id = $1 AND friend_id = $2 AND status = 'PENDING' AND action_by != $3;

-- name: DeleteFriendship :execrows
DELETE FROM friendships WHERE user_id = $1 AND friend_id = $2;

-- name: UpsertFriendshipAccepted :exec
INSERT INTO friendships (id, user_id, friend_id, status, action_by) VALUES ($1, $2, $3, 'ACCEPTED', $4)
ON CONFLICT (LEAST(user_id, friend_id), GREATEST(user_id, friend_id))
DO UPDATE SET status = 'ACCEPTED', action_by = $4, updated_at = now();

-- name: UpsertFriendshipAcceptedNoAction :exec
INSERT INTO friendships (id, user_id, friend_id, status, action_by) VALUES ($1, $2, $3, 'ACCEPTED', $4)
ON CONFLICT (LEAST(user_id, friend_id), GREATEST(user_id, friend_id))
DO UPDATE SET status = 'ACCEPTED', updated_at = now();

-- name: BlockUser :exec
INSERT INTO friendships (id, user_id, friend_id, status, action_by) VALUES ($1, $2, $3, 'BLOCKED', $4);

-- name: GetFriendInviteByToken :one
SELECT id, email, invited_by, status, expires_at FROM friend_invites WHERE token_hash = $1;

-- name: GetFriendInviteByTokenForUpdate :one
SELECT id, email, invited_by, status, expires_at FROM friend_invites WHERE token_hash = $1 FOR UPDATE;

-- name: CreateFriendInvite :exec
INSERT INTO friend_invites (id, email, token_hash, invited_by) VALUES ($1, $2, $3, $4)
ON CONFLICT (lower(email), invited_by) WHERE status = 'PENDING'
DO UPDATE SET token_hash = $3, created_at = now(), expires_at = now() + interval '7 days';

-- name: UpdateFriendInviteAccepted :exec
UPDATE friend_invites SET status = 'ACCEPTED' WHERE id = $1;

-- name: CreateFriendRequestNotification :exec
INSERT INTO notifications (user_id, type, title, body, data) VALUES ($1, 'FRIEND_REQUEST', $2, $3, $4);

-- name: GetDirectGroupByKey :one
SELECT id FROM groups WHERE direct_key = $1;

-- name: CreateDirectGroup :exec
INSERT INTO groups (id, name, currency, group_type, direct_key, created_by) VALUES ($1, $2, 'NPR', 'DIRECT', $3, $4);

-- name: AddDirectGroupMember :exec
INSERT INTO group_members (group_id, user_id, role) VALUES ($1, $2, 'OWNER') ON CONFLICT DO NOTHING;

-- name: GetDirectBalance :one
SELECT
    (COALESCE((SELECT SUM(ROUND(expenses.amount * COALESCE(expenses.exchange_rate, 1))::bigint) FROM expenses WHERE expenses.group_id = $1 AND expenses.paid_by = $2 AND expenses.deleted_at IS NULL), 0::bigint)
  - COALESCE((SELECT SUM(ROUND(s.amount * COALESCE(e.exchange_rate, 1))::bigint) FROM expense_splits s JOIN expenses e ON e.id = s.expense_id WHERE e.group_id = $1 AND s.user_id = $2 AND e.deleted_at IS NULL), 0::bigint)
  + COALESCE((SELECT SUM(settlements.amount) FROM settlements WHERE settlements.group_id = $1 AND settlements.from_user = $2 AND settlements.deleted_at IS NULL), 0::bigint)
  - COALESCE((SELECT SUM(settlements.amount) FROM settlements WHERE settlements.group_id = $1 AND settlements.to_user = $2 AND settlements.deleted_at IS NULL), 0::bigint))::bigint AS amount,
    (SELECT groups.currency FROM groups WHERE groups.id = $1)::text AS currency;

-- name: GetFriendDirectLedgerBalance :one
SELECT
    (COALESCE((SELECT SUM(ROUND(expenses.amount * COALESCE(expenses.exchange_rate, 1))::bigint) FROM expenses WHERE expenses.group_id = $1 AND expenses.paid_by = $2 AND expenses.deleted_at IS NULL), 0::bigint)
  - COALESCE((SELECT SUM(ROUND(s.amount * COALESCE(e.exchange_rate, 1))::bigint) FROM expense_splits s JOIN expenses e ON e.id = s.expense_id WHERE e.group_id = $1 AND s.user_id = $2 AND e.deleted_at IS NULL), 0::bigint)
  + COALESCE((SELECT SUM(settlements.amount) FROM settlements WHERE settlements.group_id = $1 AND settlements.from_user = $2 AND settlements.deleted_at IS NULL), 0::bigint)
  - COALESCE((SELECT SUM(settlements.amount) FROM settlements WHERE settlements.group_id = $1 AND settlements.to_user = $2 AND settlements.deleted_at IS NULL), 0::bigint))::bigint AS amount,
    (SELECT groups.currency FROM groups WHERE groups.id = $1)::text AS currency;

-- name: GetUserAvatarByID :one
SELECT coalesce(avatar_url, '') AS avatar_url FROM users WHERE id = $1;

-- name: GetFriendshipByUsers :one
SELECT status FROM friendships WHERE user_id = $1 AND friend_id = $2;
