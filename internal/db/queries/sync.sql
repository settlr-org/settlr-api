-- End-to-end-encrypted sync (internal/syncstore). The server only ever sees ciphertext.

-- name: GetSyncKey :one
SELECT kdf, kdf_params, salt, wrapped_key, version FROM sync_keys WHERE user_id = $1;

-- name: InsertSyncKey :execrows
INSERT INTO sync_keys (user_id, kdf, kdf_params, salt, wrapped_key, version)
VALUES ($1, $2, $3, $4, $5, 1)
ON CONFLICT (user_id) DO NOTHING;

-- name: UpdateSyncKey :execrows
-- Changing the passphrase re-wraps the same data key; the caller proves it saw the current version.
UPDATE sync_keys SET kdf = $2, kdf_params = $3, salt = $4, wrapped_key = $5, version = version + 1, updated_at = now()
WHERE user_id = $1 AND version = $6;

-- name: EnsureSyncCounter :exec
INSERT INTO sync_counters (user_id) VALUES ($1) ON CONFLICT (user_id) DO NOTHING;

-- name: ReserveSyncSeqs :one
-- Reserves n sequence numbers; the first one is returned.
UPDATE sync_counters SET next_seq = next_seq + @n::bigint WHERE user_id = @user_id RETURNING (next_seq - @n::bigint)::bigint AS first_seq;

-- name: UpsertSyncRecord :exec
INSERT INTO sync_records (user_id, id, seq, deleted, nonce, ciphertext)
VALUES ($1, $2, $3, $4, $5, $6)
ON CONFLICT (user_id, id) DO UPDATE SET seq = EXCLUDED.seq, deleted = EXCLUDED.deleted, nonce = EXCLUDED.nonce,
    ciphertext = EXCLUDED.ciphertext, updated_at = now();

-- name: ListSyncChanges :many
SELECT id, seq, deleted, nonce, ciphertext FROM sync_records
WHERE user_id = $1 AND seq > $2 ORDER BY seq LIMIT $3;

-- name: SyncStatus :one
SELECT COUNT(*)::bigint AS records, COALESCE(SUM(octet_length(ciphertext)), 0)::bigint AS bytes, COALESCE(MAX(seq), 0)::bigint AS latest_seq
FROM sync_records WHERE user_id = $1;

-- name: DeleteSyncRecords :exec
DELETE FROM sync_records WHERE user_id = $1;

-- name: DeleteSyncCounter :exec
DELETE FROM sync_counters WHERE user_id = $1;

-- name: DeleteSyncKey :exec
DELETE FROM sync_keys WHERE user_id = $1;
