-- +goose Up
-- A password is optional for accounts created through a verified OAuth provider.
ALTER TABLE users ALTER COLUMN password_hash DROP NOT NULL;

CREATE TABLE oauth_identities (
    provider   TEXT NOT NULL CHECK (provider IN ('google')),
    subject    TEXT NOT NULL,
    user_id    UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (provider, subject)
);

CREATE INDEX idx_oauth_identities_user_id ON oauth_identities (user_id);

-- +goose Down
DROP TABLE IF EXISTS oauth_identities;
-- This migration cannot safely make password_hash required again while OAuth-only
-- accounts exist, so it intentionally leaves the column nullable on rollback.
