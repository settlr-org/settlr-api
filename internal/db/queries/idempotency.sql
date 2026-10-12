-- Idempotency-Key reservations (see internal/idempotency).

-- name: ReserveIdempotencyKey :one
-- Inserts a pending reservation, or takes over an existing row whose key has
-- expired or whose pending lease went stale (e.g. the server died mid-request).
-- Returns no row when a live reservation/response already exists.
INSERT INTO idempotency_keys (user_id, key, request_method, request_path, request_hash, state, lock_token, created_at, expires_at)
VALUES (
    sqlc.arg(user_id), sqlc.arg(key), sqlc.arg(request_method), sqlc.arg(request_path), sqlc.arg(request_hash),
    'pending', sqlc.arg(lock_token), now(), now() + make_interval(secs => sqlc.arg(ttl_seconds)::float8)
)
ON CONFLICT (user_id, key) DO UPDATE SET
    request_method = EXCLUDED.request_method,
    request_path = EXCLUDED.request_path,
    request_hash = EXCLUDED.request_hash,
    state = 'pending',
    lock_token = EXCLUDED.lock_token,
    response_status = NULL,
    response_content_type = NULL,
    response_body = NULL,
    created_at = now(),
    expires_at = EXCLUDED.expires_at
WHERE idempotency_keys.expires_at <= now()
   OR (idempotency_keys.state = 'pending'
       AND idempotency_keys.created_at <= now() - make_interval(secs => sqlc.arg(lease_seconds)::float8))
RETURNING lock_token;

-- name: GetIdempotencyKey :one
SELECT user_id, key, request_method, request_path, request_hash, state, response_status, response_content_type, response_body, expires_at
FROM idempotency_keys
WHERE user_id = $1 AND key = $2 AND expires_at > now();

-- name: CompleteIdempotencyKey :execrows
UPDATE idempotency_keys
SET state = 'completed', response_status = $4, response_content_type = $5, response_body = $6
WHERE user_id = $1 AND key = $2 AND lock_token = $3 AND state = 'pending';

-- name: ReleaseIdempotencyKey :exec
DELETE FROM idempotency_keys
WHERE user_id = $1 AND key = $2 AND lock_token = $3 AND state = 'pending';

-- name: DeleteExpiredIdempotencyKeys :execrows
DELETE FROM idempotency_keys WHERE expires_at <= now();
