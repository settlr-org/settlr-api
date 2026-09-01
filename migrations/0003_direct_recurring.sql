-- +goose Up
-- Phase 3: 1:1 friend ledgers (DIRECT groups) + recurring expenses

-- Allow DIRECT group type (1:1 ledger between two friends)
ALTER TABLE groups DROP CONSTRAINT groups_group_type_check;
ALTER TABLE groups ADD CONSTRAINT groups_group_type_check CHECK (group_type IN ('HOME','TRIP','COUPLE','OTHER','DIRECT'));

-- Deterministic link between a friendship pair and its shared ledger group
ALTER TABLE groups ADD COLUMN direct_key TEXT UNIQUE;

-- Recurring expense templates; a scheduler materializes due rows into expenses
CREATE TABLE recurring_expenses (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    group_id     UUID NOT NULL REFERENCES groups(id) ON DELETE CASCADE,
    created_by   UUID NOT NULL REFERENCES users(id),
    description  TEXT NOT NULL CHECK (char_length(description) BETWEEN 1 AND 500),
    amount       BIGINT NOT NULL CHECK (amount > 0),
    currency     CHAR(3) NOT NULL DEFAULT 'USD',
    category_id  UUID REFERENCES categories(id),
    split_mode   TEXT NOT NULL DEFAULT 'EQUAL' CHECK (split_mode IN ('EQUAL','EXACT','PERCENTAGE','SHARES')),
    splits       JSONB NOT NULL DEFAULT '[]'::jsonb,
    paid_by      UUID NOT NULL REFERENCES users(id),
    frequency    TEXT NOT NULL CHECK (frequency IN ('WEEKLY','MONTHLY','YEARLY')),
    next_run_at  TIMESTAMPTZ NOT NULL,
    last_run_at  TIMESTAMPTZ,
    active       BOOLEAN NOT NULL DEFAULT true,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_recurring_next_run ON recurring_expenses(active, next_run_at);
CREATE INDEX idx_recurring_group ON recurring_expenses(group_id);

-- +goose Down
-- Revert Phase 3: recurring expenses + DIRECT groups
DROP TABLE IF EXISTS recurring_expenses;
ALTER TABLE groups DROP COLUMN IF EXISTS direct_key;
ALTER TABLE groups DROP CONSTRAINT groups_group_type_check;
ALTER TABLE groups ADD CONSTRAINT groups_group_type_check CHECK (group_type IN ('HOME','TRIP','COUPLE','OTHER'));
