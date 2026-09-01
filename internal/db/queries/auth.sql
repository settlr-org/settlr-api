-- Auth domain queries for sqlc with pgx/v5.
-- These replace manual Pool.Query/Exec calls in internal/auth/handlers.go and internal/auth/auth.go
-- Handler usage: Handler{Svc *auth.Service, Queries *db.Queries} or Service{Pool *pgxpool.Pool, Queries *db.Queries}

-- name: GetUserByEmail :one
SELECT id, name, email FROM users WHERE lower(email) = lower($1) LIMIT 1;

-- name: GetUserByEmailForUpdate :one
SELECT id, name, email FROM users WHERE lower(email) = lower($1) LIMIT 1 FOR UPDATE;

-- name: GetUserByID :one
SELECT id, name, email FROM users WHERE id = $1 LIMIT 1;

-- name: GetUserByEmailForLogin :one
SELECT id, name, email, password_hash, email_verified_at FROM users WHERE lower(email) = lower($1) LIMIT 1;

-- name: GetUserIDByEmail :one
SELECT id FROM users WHERE lower(email) = lower($1) LIMIT 1;

-- name: GetOAuthIdentityForUpdate :one
SELECT u.id, u.name, u.email FROM oauth_identities oi JOIN users u ON u.id = oi.user_id WHERE oi.provider = 'google' AND oi.subject = $1 FOR UPDATE;

-- name: CreateUser :exec
INSERT INTO users (id, name, email, password_hash) VALUES ($1, $2, $3, $4);

-- name: CreateOAuthUser :exec
INSERT INTO users (id, name, email, password_hash, email_verified_at) VALUES ($1, $2, $3, NULL, now());

-- name: CreateOAuthIdentity :exec
INSERT INTO oauth_identities (provider, subject, user_id) VALUES ('google', $1, $2);

-- name: SetUserEmailVerifiedIfNull :exec
UPDATE users SET email_verified_at = COALESCE(email_verified_at, now()), updated_at = now() WHERE id = $1;

-- name: CreateEmailVerificationToken :exec
INSERT INTO email_verification_tokens (user_id, token_hash, expires_at) VALUES ($1, $2, now() + interval '24 hours');

-- name: GetEmailVerificationTokenUser :one
SELECT user_id FROM email_verification_tokens WHERE token_hash = $1 AND expires_at > now() AND verified_at IS NULL LIMIT 1;

-- name: SetEmailVerified :exec
UPDATE users SET email_verified_at = now(), updated_at = now() WHERE id = $1;

-- name: MarkEmailVerificationVerified :exec
UPDATE email_verification_tokens SET verified_at = now() WHERE token_hash = $1;

-- name: GetUserVerificationByID :one
SELECT email, email_verified_at FROM users WHERE id = $1 LIMIT 1;

-- name: GetUserVerificationByEmail :one
SELECT email, email_verified_at FROM users WHERE lower(email) = lower($1) LIMIT 1;

-- name: CreatePasswordResetToken :exec
INSERT INTO password_reset_tokens (user_id, token_hash, expires_at) VALUES ($1, $2, now() + interval '1 hour');

-- name: GetPasswordResetTokenUser :one
SELECT user_id FROM password_reset_tokens WHERE token_hash = $1 AND expires_at > now() AND used_at IS NULL LIMIT 1;

-- name: UpdateUserPassword :exec
UPDATE users SET password_hash = $1, updated_at = now() WHERE id = $2;

-- name: MarkPasswordResetUsed :exec
UPDATE password_reset_tokens SET used_at = now() WHERE token_hash = $1;

-- name: RevokeAllUserSessions :exec
UPDATE sessions SET revoked_at = now() WHERE user_id = $1 AND revoked_at IS NULL;

-- name: CreateSession :exec
INSERT INTO sessions (user_id, refresh_token_hash, user_agent, ip, expires_at) VALUES ($1, $2, $3, $4, $5);

-- name: GetSessionUserByHash :one
SELECT user_id FROM sessions WHERE refresh_token_hash = $1 LIMIT 1;


-- name: RevokeSessionByHash :exec
UPDATE sessions SET revoked_at = now() WHERE refresh_token_hash = $1;

-- name: RevokeSessionByID :exec
UPDATE sessions SET revoked_at = now() WHERE id = $1 AND user_id = $2 AND revoked_at IS NULL;

-- name: RevokeSessionByIDReturning :one
UPDATE sessions SET revoked_at = now() WHERE id = $1 AND user_id = $2 AND revoked_at IS NULL RETURNING id;

-- name: ListSessionsByUser :many
SELECT id, user_agent, ip, created_at, last_used_at, expires_at, revoked_at FROM sessions WHERE user_id = $1 ORDER BY created_at DESC;

-- name: RotateSession :one
WITH rotated AS (
    UPDATE sessions SET revoked_at = now() WHERE sessions.refresh_token_hash = $1 AND sessions.revoked_at IS NULL RETURNING sessions.user_id, sessions.id
)
INSERT INTO sessions (user_id, refresh_token_hash, expires_at, rotated_from)
SELECT rotated.user_id, $2, now() + interval '30 days', rotated.id FROM rotated
RETURNING id;
