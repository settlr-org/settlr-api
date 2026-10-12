-- +goose Up
-- Idempotency-Key support for authenticated mutating requests. A row is reserved
-- ('pending') before the handler runs and then completed with the captured
-- response so retries of the same (user, key) replay it instead of re-executing.
CREATE TABLE idempotency_keys (
    user_id               UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    key                   TEXT NOT NULL CHECK (char_length(key) BETWEEN 1 AND 128),
    request_method        TEXT NOT NULL,
    request_path          TEXT NOT NULL,
    request_hash          TEXT NOT NULL,
    state                 TEXT NOT NULL DEFAULT 'pending' CHECK (state IN ('pending', 'completed')),
    -- Identifies the request holding the reservation, so a request whose lease
    -- was taken over cannot complete or release someone else's row.
    lock_token            UUID NOT NULL,
    response_status       INT,
    response_content_type TEXT,
    response_body         BYTEA,
    created_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at            TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (user_id, key)
);

CREATE INDEX idx_idempotency_keys_expires_at ON idempotency_keys (expires_at);

-- +goose Down
DROP TABLE IF EXISTS idempotency_keys;
