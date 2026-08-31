-- Settlr initial schema.
-- All monetary amounts are BIGINT in the smallest currency unit (e.g. cents).

CREATE EXTENSION IF NOT EXISTS "pgcrypto";

CREATE TABLE users (
    id                 UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name               TEXT NOT NULL,
    email              TEXT NOT NULL,
    password_hash      TEXT NOT NULL,
    avatar_url         TEXT,
    default_currency   CHAR(3) NOT NULL DEFAULT 'USD',
    timezone           TEXT NOT NULL DEFAULT 'UTC',
    email_verified_at  TIMESTAMPTZ,
    is_anonymous       BOOLEAN NOT NULL DEFAULT FALSE,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX idx_users_email ON users (lower(email));

CREATE TABLE sessions (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id             UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    refresh_token_hash  TEXT NOT NULL UNIQUE,
    user_agent          TEXT,
    ip                  TEXT,
    rotated_from        UUID REFERENCES sessions(id) ON DELETE SET NULL,
    expires_at          TIMESTAMPTZ NOT NULL,
    revoked_at          TIMESTAMPTZ,
    last_used_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_sessions_user_id ON sessions (user_id);

CREATE TABLE password_reset_tokens (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id      UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    token_hash   TEXT NOT NULL UNIQUE,
    expires_at   TIMESTAMPTZ NOT NULL,
    used_at      TIMESTAMPTZ,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_password_reset_tokens_user_id ON password_reset_tokens (user_id);

CREATE TABLE email_verification_tokens (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id      UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    token_hash   TEXT NOT NULL UNIQUE,
    expires_at   TIMESTAMPTZ NOT NULL,
    verified_at  TIMESTAMPTZ,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_email_verification_tokens_user_id ON email_verification_tokens (user_id);

CREATE TABLE groups (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name        TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    avatar_url  TEXT,
    currency    CHAR(3) NOT NULL DEFAULT 'USD',
    created_by  UUID NOT NULL REFERENCES users(id),
    archived_at TIMESTAMPTZ,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_groups_created_by ON groups (created_by);

CREATE TABLE group_members (
    group_id   UUID NOT NULL REFERENCES groups(id) ON DELETE CASCADE,
    user_id    UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    role       TEXT NOT NULL DEFAULT 'MEMBER' CHECK (role IN ('OWNER', 'ADMIN', 'MEMBER')),
    joined_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (group_id, user_id)
);

CREATE INDEX idx_group_members_group_id ON group_members (group_id);
CREATE INDEX idx_group_members_user_id ON group_members (user_id);

CREATE TABLE friendships (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id       UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    friend_id     UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    status        TEXT NOT NULL CHECK (status IN ('PENDING', 'ACCEPTED', 'BLOCKED')),
    action_by     UUID REFERENCES users(id),
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (user_id <> friend_id)
);

-- One relationship row per unordered pair; canonical ordering enforced in app layer.
CREATE UNIQUE INDEX idx_friendships_pair ON friendships (LEAST(user_id, friend_id), GREATEST(user_id, friend_id));
CREATE INDEX idx_friendships_user_id ON friendships (user_id);
CREATE INDEX idx_friendships_friend_id ON friendships (friend_id);

CREATE TABLE categories (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name        TEXT NOT NULL,
    icon        TEXT NOT NULL DEFAULT 'tag',
    color       TEXT NOT NULL DEFAULT '#6B7280',
    is_system   BOOLEAN NOT NULL DEFAULT FALSE,
    owner_id    UUID REFERENCES users(id) ON DELETE CASCADE,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_categories_owner_id ON categories (owner_id);
CREATE UNIQUE INDEX idx_categories_system_name ON categories (lower(name)) WHERE is_system;

CREATE TABLE expenses (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    group_id      UUID NOT NULL REFERENCES groups(id),
    description   TEXT NOT NULL,
    amount        BIGINT NOT NULL CHECK (amount > 0),
    currency      CHAR(3) NOT NULL,
    split_mode    TEXT NOT NULL CHECK (split_mode IN ('EQUAL', 'EXACT', 'PERCENTAGE', 'SHARES')),
    paid_by       UUID NOT NULL REFERENCES users(id),
    category_id   UUID REFERENCES categories(id) ON DELETE SET NULL,
    notes         TEXT NOT NULL DEFAULT '',
    expense_date  DATE NOT NULL,
    created_by    UUID NOT NULL REFERENCES users(id),
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at    TIMESTAMPTZ
);

CREATE INDEX idx_expenses_group_date ON expenses (group_id, expense_date DESC);
CREATE INDEX idx_expenses_paid_by ON expenses (paid_by);
CREATE INDEX idx_expenses_category_id ON expenses (category_id);

CREATE TABLE expense_splits (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    expense_id   UUID NOT NULL REFERENCES expenses(id) ON DELETE CASCADE,
    user_id      UUID NOT NULL REFERENCES users(id),
    amount       BIGINT NOT NULL CHECK (amount >= 0),
    percentage   NUMERIC(7,4),
    shares       INTEGER,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_expense_splits_expense_id ON expense_splits (expense_id);
CREATE INDEX idx_expense_splits_user_id ON expense_splits (user_id);

CREATE TABLE settlements (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    group_id    UUID NOT NULL REFERENCES groups(id),
    from_user   UUID NOT NULL REFERENCES users(id),
    to_user     UUID NOT NULL REFERENCES users(id),
    amount      BIGINT NOT NULL CHECK (amount > 0),
    currency    CHAR(3) NOT NULL,
    note        TEXT NOT NULL DEFAULT '',
    settled_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_by  UUID NOT NULL REFERENCES users(id),
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at  TIMESTAMPTZ,
    CHECK (from_user <> to_user)
);

CREATE INDEX idx_settlements_group_id ON settlements (group_id);
CREATE INDEX idx_settlements_from_user ON settlements (from_user);
CREATE INDEX idx_settlements_to_user ON settlements (to_user);

CREATE TABLE notifications (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id     UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    type        TEXT NOT NULL CHECK (type IN ('FRIEND_REQUEST','GROUP_INVITATION','EXPENSE_ADDED','EXPENSE_UPDATED','SETTLEMENT_RECORDED','MENTION')),
    title       TEXT NOT NULL,
    body        TEXT NOT NULL DEFAULT '',
    data        JSONB NOT NULL DEFAULT '{}',
    read_at     TIMESTAMPTZ,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_notifications_user_created ON notifications (user_id, created_at DESC);

CREATE TABLE activity_events (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    group_id     UUID REFERENCES groups(id) ON DELETE CASCADE,
    actor_id     UUID REFERENCES users(id),
    type         TEXT NOT NULL,
    entity_type  TEXT NOT NULL,
    entity_id    UUID,
    payload      JSONB NOT NULL DEFAULT '{}',
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_activity_events_group_created ON activity_events (group_id, created_at DESC);
