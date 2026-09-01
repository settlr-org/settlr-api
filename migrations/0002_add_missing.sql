-- +goose Up
-- Phase 2 schema expansion: group invites, comments, attachments, per-group simplify, currency conversion

-- Groups: add simplify toggle, type, invite token
ALTER TABLE groups
    ADD COLUMN simplify_debts BOOLEAN NOT NULL DEFAULT true,
    ADD COLUMN group_type TEXT NOT NULL DEFAULT 'OTHER' CHECK (group_type IN ('HOME','TRIP','COUPLE','OTHER')),
    ADD COLUMN invite_token TEXT UNIQUE;

-- Group invites for non-registered emails (placeholder invites)
CREATE TABLE group_invites (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    group_id    UUID NOT NULL REFERENCES groups(id) ON DELETE CASCADE,
    email       TEXT NOT NULL,
    token_hash  TEXT NOT NULL UNIQUE,
    invited_by  UUID NOT NULL REFERENCES users(id),
    status      TEXT NOT NULL DEFAULT 'PENDING' CHECK (status IN ('PENDING','ACCEPTED','EXPIRED')),
    expires_at  TIMESTAMPTZ NOT NULL DEFAULT now() + interval '7 days',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_group_invites_group_id ON group_invites(group_id);
CREATE INDEX idx_group_invites_email ON group_invites(lower(email));
CREATE UNIQUE INDEX idx_group_invites_group_email ON group_invites(group_id, lower(email)) WHERE status='PENDING';

-- Expense comments (thread)
CREATE TABLE expense_comments (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    expense_id  UUID NOT NULL REFERENCES expenses(id) ON DELETE CASCADE,
    user_id     UUID NOT NULL REFERENCES users(id),
    body        TEXT NOT NULL CHECK (char_length(body) BETWEEN 1 AND 2000),
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at  TIMESTAMPTZ
);
CREATE INDEX idx_expense_comments_expense_id ON expense_comments(expense_id);
CREATE INDEX idx_expense_comments_user_id ON expense_comments(user_id);

-- Expense attachments / receipts
CREATE TABLE expense_attachments (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    expense_id  UUID NOT NULL REFERENCES expenses(id) ON DELETE CASCADE,
    user_id     UUID NOT NULL REFERENCES users(id),
    file_url    TEXT NOT NULL,
    file_name   TEXT NOT NULL DEFAULT '',
    mime_type   TEXT NOT NULL DEFAULT 'application/octet',
    size_bytes  INTEGER NOT NULL DEFAULT 0 CHECK (size_bytes >= 0),
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_expense_attachments_expense_id ON expense_attachments(expense_id);

-- Multi-currency conversion fields (nullable, used when expense currency != group currency)
ALTER TABLE expenses
    ADD COLUMN exchange_rate NUMERIC(12,6),
    ADD COLUMN base_currency CHAR(3),
    ADD COLUMN base_amount BIGINT CHECK (base_amount IS NULL OR base_amount > 0);

-- Notifications preferences per user (optional, defaults to all enabled)
CREATE TABLE notification_preferences (
    user_id     UUID PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    email_enabled BOOLEAN NOT NULL DEFAULT true,
    push_enabled  BOOLEAN NOT NULL DEFAULT true,
    friend_request_enabled BOOLEAN NOT NULL DEFAULT true,
    expense_enabled BOOLEAN NOT NULL DEFAULT true,
    settlement_enabled BOOLEAN NOT NULL DEFAULT true,
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE IF EXISTS expense_attachments;
DROP TABLE IF EXISTS expense_comments;
DROP TABLE IF EXISTS group_invites;
DROP TABLE IF EXISTS notification_preferences;
ALTER TABLE expenses DROP COLUMN IF EXISTS exchange_rate;
ALTER TABLE expenses DROP COLUMN IF EXISTS base_currency;
ALTER TABLE expenses DROP COLUMN IF EXISTS base_amount;
ALTER TABLE groups DROP COLUMN IF EXISTS simplify_debts;
ALTER TABLE groups DROP COLUMN IF EXISTS group_type;
ALTER TABLE groups DROP COLUMN IF EXISTS invite_token;
