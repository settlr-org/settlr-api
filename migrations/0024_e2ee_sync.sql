-- +goose Up
-- End-to-end-encrypted sync of the on-device wallet (docs/design/local-first.md, "Sync"). The server stores opaque
-- ciphertext: it cannot read, search or compute on any of it. Records are identified by client-chosen UUIDs and ordered
-- by a per-user sequence so devices can pull "everything after N".
CREATE TABLE sync_keys (
    user_id     UUID PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    kdf         TEXT NOT NULL,
    kdf_params  JSONB NOT NULL,
    salt        BYTEA NOT NULL,
    wrapped_key BYTEA NOT NULL,
    version     INT NOT NULL DEFAULT 1,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE sync_counters (
    user_id  UUID PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    next_seq BIGINT NOT NULL DEFAULT 1
);

CREATE TABLE sync_records (
    user_id    UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    id         UUID NOT NULL,
    seq        BIGINT NOT NULL,
    deleted    BOOLEAN NOT NULL DEFAULT false,
    nonce      BYTEA NOT NULL,
    ciphertext BYTEA NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, id)
);
CREATE INDEX idx_sync_records_seq ON sync_records (user_id, seq);

-- +goose Down
DROP TABLE sync_records;
DROP TABLE sync_counters;
DROP TABLE sync_keys;
