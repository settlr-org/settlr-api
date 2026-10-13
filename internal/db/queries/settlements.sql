-- Settlements domain queries for sqlc with pgx/v5.
-- These replace manual Pool.Query/Exec calls in internal/settlements/handlers.go
-- Handler usage: Handler{Pool *pgxpool.Pool, Queries *db.Queries} where Queries wraps the pool.
-- Transaction usage: qtx := h.Queries.WithTx(tx)

-- name: CheckSettlementIsGroupMember :one
SELECT EXISTS(SELECT 1 FROM group_members WHERE group_id = $1 AND user_id = $2) AS is_member;

-- name: GetSettlementGroupCurrency :one
SELECT currency FROM groups WHERE id = $1;

-- name: CreateSettlement :exec
INSERT INTO settlements (id, group_id, from_user, to_user, amount, currency, note, settled_at, created_by)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9);

-- name: CreateSettlementActivity :exec
INSERT INTO activity_events (group_id, actor_id, type, entity_type, entity_id, payload) VALUES ($1, $2, 'SETTLEMENT_RECORDED', 'settlement', $3, $4);

-- name: CreateSettlementNotification :exec
INSERT INTO notifications (user_id, type, title, body, data) VALUES ($1, 'SETTLEMENT_RECORDED', $2, $3, $4);

-- name: ListSettlements :many
-- account_id is the caller's own wallet account for the settlement (payer side:
-- from_account_id, receiver side: settlement_accounts), never another user's.
SELECT s.id, s.from_user, s.to_user, s.amount, s.currency, s.note, s.settled_at, s.created_by, s.created_at,
       (CASE WHEN s.from_user = @user_id::uuid THEN s.from_account_id
             WHEN s.to_user = @user_id::uuid THEN sa.account_id END)::uuid AS account_id
FROM settlements s
LEFT JOIN settlement_accounts sa ON sa.settlement_id = s.id AND sa.user_id = @user_id::uuid
WHERE s.group_id = @group_id AND s.deleted_at IS NULL
ORDER BY s.settled_at DESC, s.created_at DESC
LIMIT @lim::int;

-- name: GetSettlementForUpdate :one
SELECT group_id, deleted_at FROM settlements WHERE id = $1;

-- name: UpdateSettlementAmount :exec
UPDATE settlements SET amount = $1, updated_at = now() WHERE id = $2;

-- name: UpdateSettlementNote :exec
UPDATE settlements SET note = $1, updated_at = now() WHERE id = $2;

-- name: GetSettlementForDelete :one
SELECT group_id FROM settlements WHERE id = $1 AND deleted_at IS NULL;

-- name: SoftDeleteSettlement :exec
UPDATE settlements SET deleted_at = now(), updated_at = now() WHERE id = $1;
