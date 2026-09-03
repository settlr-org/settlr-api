-- Groups domain queries for sqlc with pgx/v5.
-- These replace manual Pool.Query/Exec calls in internal/groups/handlers.go
-- Handler usage: Handler{Pool *pgxpool.Pool, Queries *db.Queries} where Queries wraps the pool.

-- name: IsMember :one
-- Checks membership and returns role if present. Used ~30+ times across handlers.
SELECT role FROM group_members WHERE group_id = $1 AND user_id = $2;

-- name: GetMemberRole :one
SELECT role FROM group_members WHERE group_id = $1 AND user_id = $2;

-- name: ListGroups :many
SELECT
    g.id,
    g.name,
    g.description,
    coalesce(g.avatar_url, '') AS avatar_url,
    g.currency,
    coalesce(g.group_type, 'OTHER') AS group_type,
    coalesce(g.simplify_debts, true) AS simplify_debts,
    g.created_by,
    g.created_at,
    g.updated_at,
    g.archived_at,
    g.information
FROM groups g
JOIN group_members gm ON gm.group_id = g.id
WHERE gm.user_id = $1
  AND g.archived_at IS NULL
  AND g.group_type <> 'DIRECT'
ORDER BY g.updated_at DESC;

-- name: ListGroupsFiltered :many
SELECT
    g.id,
    g.name,
    g.description,
    coalesce(g.avatar_url, '') AS avatar_url,
    g.currency,
    coalesce(g.group_type, 'OTHER') AS group_type,
    coalesce(g.simplify_debts, true) AS simplify_debts,
    g.created_by,
    g.created_at,
    g.updated_at,
    g.archived_at,
    g.information
FROM groups g
JOIN group_members gm ON gm.group_id = g.id
WHERE gm.user_id = $1
  AND g.archived_at IS NULL
  AND g.group_type <> 'DIRECT'
  AND g.name ILIKE '%' || $2 || '%'
ORDER BY g.updated_at DESC;

-- name: GetGroup :one
SELECT
    g.name,
    g.description,
    coalesce(g.avatar_url, '') AS avatar_url,
    g.currency,
    coalesce(g.group_type, 'OTHER') AS group_type,
    coalesce(g.simplify_debts, true) AS simplify_debts,
    g.created_by,
    g.created_at,
    g.updated_at,
    g.archived_at,
    g.information
FROM groups g
WHERE g.id = $1;

-- name: CreateGroup :exec
INSERT INTO groups (id, name, description, avatar_url, currency, group_type, created_by, information)
VALUES ($1, $2, $3, NULLIF($4, ''), $5, $6, $7, $8);

-- name: CreateGroupMember :exec
INSERT INTO group_members (group_id, user_id, role) VALUES ($1, $2, $3);

-- name: InsertActivityEvent :exec
INSERT INTO activity_events (group_id, actor_id, type, entity_type, entity_id, payload)
VALUES ($1, $2, $3, $4, $5, $6);

-- name: UpdateGroupName :exec
UPDATE groups SET name = $2, updated_at = now() WHERE id = $1;

-- name: UpdateGroupDescription :exec
UPDATE groups SET description = $2, updated_at = now() WHERE id = $1;

-- name: UpdateGroupAvatar :exec
UPDATE groups SET avatar_url = $2, updated_at = now() WHERE id = $1;

-- name: UpdateGroupCurrency :exec
UPDATE groups SET currency = $2, updated_at = now() WHERE id = $1;

-- name: UpdateGroupType :exec
UPDATE groups SET group_type = $2, updated_at = now() WHERE id = $1;

-- name: UpdateGroupSimplifyDebts :exec
UPDATE groups SET simplify_debts = $2, updated_at = now() WHERE id = $1;

-- name: UpdateGroupInformation :exec
UPDATE groups SET information = $2, updated_at = now() WHERE id = $1;

-- name: ListGroupMembers :many
SELECT
    u.id,
    u.name,
    coalesce(u.avatar_url, '') AS avatar_url,
    gm.role,
    gm.joined_at
FROM group_members gm
JOIN users u ON u.id = gm.user_id
WHERE gm.group_id = $1
ORDER BY gm.joined_at;

-- name: CheckFriendship :one
SELECT EXISTS(
    SELECT 1 FROM friendships
    WHERE LEAST(user_id, friend_id) = LEAST($1::uuid, $2::uuid)
      AND GREATEST(user_id, friend_id) = GREATEST($1::uuid, $2::uuid)
      AND status = 'ACCEPTED'
) AS is_friend;

-- name: AddGroupMember :exec
INSERT INTO group_members (group_id, user_id, role) VALUES ($1, $2, $3) ON CONFLICT DO NOTHING;

-- name: RemoveGroupMember :exec
DELETE FROM group_members WHERE group_id = $1 AND user_id = $2;

-- name: UpdateMemberRole :exec
UPDATE group_members SET role = $3 WHERE group_id = $1 AND user_id = $2;

-- name: GetGroupMember :one
SELECT u.name, coalesce(u.avatar_url, '') AS avatar_url, gm.joined_at
FROM group_members gm
JOIN users u ON u.id = gm.user_id
WHERE gm.group_id = $1 AND gm.user_id = $2;

-- name: GetInviteToken :one
SELECT invite_token FROM groups WHERE id = $1;

-- name: SetInviteToken :exec
UPDATE groups SET invite_token = $2 WHERE id = $1;

-- name: ArchiveGroup :exec
UPDATE groups SET archived_at = now(), updated_at = now() WHERE id = $1;

-- name: DeleteGroup :exec
DELETE FROM groups WHERE id = $1;

-- name: DeleteGroupSettlements :exec
DELETE FROM settlements WHERE group_id = $1;

-- name: DeleteGroupExpenses :exec
DELETE FROM expenses WHERE group_id = $1;

-- name: DeleteGroupExpenseSplits :exec
DELETE FROM expense_splits WHERE expense_id IN (SELECT id FROM expenses WHERE group_id = $1);

-- name: DeleteGroupExpenseAttachments :exec
DELETE FROM expense_attachments WHERE expense_id IN (SELECT id FROM expenses WHERE group_id = $1);

-- name: DeleteGroupRecurring :exec
DELETE FROM recurring_expenses WHERE group_id = $1;

-- name: CreateGroupInvite :exec
INSERT INTO group_invites (id, group_id, email, token_hash, invited_by)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (group_id, lower(email)) WHERE status = 'PENDING'
DO UPDATE SET token_hash = $4, invited_by = $5, created_at = now(), expires_at = now() + interval '7 days';

-- name: ListGroupInvites :many
SELECT id, email, status, invited_by, created_at, expires_at
FROM group_invites
WHERE group_id = $1 AND status = 'PENDING'
ORDER BY created_at DESC;

-- name: GetUserEmail :one
SELECT lower(email) FROM users WHERE id = $1;

-- name: GetGroupInviteeIDByEmail :one
SELECT id FROM users WHERE lower(email) = lower($1);

-- name: CheckGroupMemberEmail :one
SELECT EXISTS(
    SELECT 1
    FROM users u
    JOIN group_members gm ON gm.user_id = u.id
    WHERE gm.group_id = $1 AND lower(u.email) = lower($2)
) AS already_member;

-- name: CreateGroupInviteNotification :exec
INSERT INTO notifications (user_id, type, title, body, data)
VALUES ($1, 'GROUP_INVITATION', $2, $3, $4);

-- name: GetGroupName :one
SELECT name FROM groups WHERE id = $1;

-- name: ListGroupActivity :many
SELECT id, actor_id, type, entity_type, entity_id, payload, created_at
FROM activity_events ae
WHERE ae.group_id = $1
ORDER BY created_at DESC, id DESC
LIMIT $2;

-- name: ListGroupActivityBefore :many
SELECT id, actor_id, type, entity_type, entity_id, payload, created_at
FROM activity_events ae
WHERE ae.group_id = $1
  AND (ae.created_at, ae.id) < (SELECT cursor.created_at, cursor.id FROM activity_events cursor WHERE cursor.id = $2)
ORDER BY created_at DESC, id DESC
LIMIT $3;

-- name: ListMyInvites :many
SELECT gi.id, gi.group_id, g.name AS group_name, gi.email, gi.created_at
FROM group_invites gi
JOIN groups g ON g.id = gi.group_id
WHERE lower(gi.email) = lower($1) AND gi.status = 'PENDING' AND gi.expires_at > now();

-- name: GetInviteByHash :one
SELECT id, group_id, email, invited_by, status FROM group_invites WHERE token_hash = $1;

-- name: GetInviteByHashForUpdate :one
SELECT id, group_id, email, invited_by, status
FROM group_invites
WHERE token_hash = $1
FOR UPDATE;

-- name: IsGroupInviteCurrent :one
SELECT expires_at > now() FROM group_invites WHERE id = $1;

-- name: AcceptGroupInviteMember :exec
INSERT INTO group_members (group_id, user_id, role) VALUES ($1, $2, 'MEMBER');

-- name: MarkInviteAccepted :exec
UPDATE group_invites SET status = 'ACCEPTED' WHERE id = $1 AND status = 'PENDING';

-- name: EnsureFriendship :exec
INSERT INTO friendships (id, user_id, friend_id, status, action_by)
VALUES ($1, LEAST($2::uuid, $3::uuid), GREATEST($2::uuid, $3::uuid), 'ACCEPTED', $3)
ON CONFLICT (LEAST(user_id, friend_id), GREATEST(user_id, friend_id))
DO UPDATE SET status = 'ACCEPTED', action_by = $3, updated_at = now();

-- name: CheckAlreadyMember :one
SELECT EXISTS(SELECT 1 FROM group_members WHERE group_id = $1 AND user_id = $2) AS already_member;

-- name: LeaveGroup :exec
DELETE FROM group_members WHERE group_id = $1 AND user_id = $2;
