-- +goose Up
-- Wallet phase 1 (docs/design/wallet.md): accounts, transactions, tags, rate
-- overrides, settings, unified-ledger links on group expenses/settlements, and
-- the migration of personal_expenses into transactions.

CREATE TABLE accounts (
    id                 UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id            UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name               TEXT NOT NULL CHECK (char_length(name) BETWEEN 1 AND 100),
    currency           CHAR(3) NOT NULL,
    color              TEXT NOT NULL DEFAULT '#6B7280',
    icon               TEXT NOT NULL DEFAULT 'wallet',
    include_in_balance BOOLEAN NOT NULL DEFAULT true,
    -- The account the legacy /personal API writes to (at most one live per user).
    is_default         BOOLEAN NOT NULL DEFAULT false,
    sort_order         INT NOT NULL DEFAULT 0,
    archived_at        TIMESTAMPTZ,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at         TIMESTAMPTZ
);
CREATE INDEX idx_accounts_user ON accounts (user_id, sort_order) WHERE deleted_at IS NULL;
CREATE UNIQUE INDEX idx_accounts_user_default ON accounts (user_id) WHERE is_default AND deleted_at IS NULL;

CREATE TABLE transactions (
    id                 UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id            UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    type               TEXT NOT NULL CHECK (type IN ('INCOME', 'EXPENSE', 'TRANSFER')),
    account_id         UUID REFERENCES accounts(id) ON DELETE CASCADE,
    to_account_id      UUID REFERENCES accounts(id) ON DELETE CASCADE,
    amount             BIGINT NOT NULL CHECK (amount > 0),
    currency           CHAR(3) NOT NULL,
    to_amount          BIGINT CHECK (to_amount IS NULL OR to_amount > 0),
    category_id        UUID REFERENCES categories(id) ON DELETE SET NULL,
    title              TEXT NOT NULL DEFAULT '',
    description        TEXT NOT NULL DEFAULT '',
    occurred_at        TIMESTAMPTZ,
    due_at             TIMESTAMPTZ,
    -- Phase 2 links (FKs are added when those tables exist).
    planned_payment_id UUID,
    loan_id            UUID,
    loan_record_id     UUID,
    source             TEXT NOT NULL DEFAULT 'MANUAL'
                       CHECK (source IN ('MANUAL', 'ADJUSTMENT', 'LOAN', 'IMPORT', 'PERSONAL_LEGACY')),
    import_ref         TEXT,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at         TIMESTAMPTZ,
    CHECK (occurred_at IS NOT NULL OR due_at IS NOT NULL),
    CHECK (account_id IS NOT NULL OR occurred_at IS NULL),
    CHECK (type <> 'TRANSFER' OR (account_id IS NOT NULL AND to_account_id IS NOT NULL
                                  AND to_amount IS NOT NULL AND to_account_id <> account_id)),
    CHECK (type = 'TRANSFER' OR (to_account_id IS NULL AND to_amount IS NULL))
);
CREATE INDEX idx_transactions_user_occurred ON transactions (user_id, occurred_at DESC, id DESC) WHERE deleted_at IS NULL;
CREATE INDEX idx_transactions_account ON transactions (account_id);
CREATE INDEX idx_transactions_to_account ON transactions (to_account_id) WHERE to_account_id IS NOT NULL;
CREATE INDEX idx_transactions_category ON transactions (category_id);
CREATE INDEX idx_transactions_user_planned ON transactions (user_id, due_at) WHERE occurred_at IS NULL AND deleted_at IS NULL;
CREATE INDEX idx_transactions_planned_payment ON transactions (planned_payment_id) WHERE planned_payment_id IS NOT NULL;
CREATE INDEX idx_transactions_loan ON transactions (loan_id) WHERE loan_id IS NOT NULL;
CREATE INDEX idx_transactions_import_ref ON transactions (user_id, import_ref) WHERE import_ref IS NOT NULL;

CREATE TABLE tags (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id    UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name       TEXT NOT NULL CHECK (char_length(name) BETWEEN 1 AND 50),
    color      TEXT NOT NULL DEFAULT '#6B7280',
    icon       TEXT NOT NULL DEFAULT 'tag',
    sort_order INT NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX idx_tags_user_name ON tags (user_id, lower(name));

CREATE TABLE transaction_tags (
    transaction_id UUID NOT NULL REFERENCES transactions(id) ON DELETE CASCADE,
    tag_id         UUID NOT NULL REFERENCES tags(id) ON DELETE CASCADE,
    PRIMARY KEY (transaction_id, tag_id)
);
CREATE INDEX idx_transaction_tags_tag ON transaction_tags (tag_id);

CREATE TABLE exchange_rate_overrides (
    user_id    UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    base       CHAR(3) NOT NULL,
    quote      CHAR(3) NOT NULL,
    rate       NUMERIC(24,10) NOT NULL CHECK (rate > 0),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, base, quote),
    CHECK (base <> quote)
);

CREATE TABLE user_settings (
    user_id                 UUID PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    month_start_day         INT NOT NULL DEFAULT 1 CHECK (month_start_day BETWEEN 1 AND 28),
    hide_balance            BOOLEAN NOT NULL DEFAULT false,
    hide_income             BOOLEAN NOT NULL DEFAULT false,
    treat_transfers_as_flow BOOLEAN NOT NULL DEFAULT false,
    theme                   TEXT NOT NULL DEFAULT 'auto' CHECK (theme IN ('light', 'dark', 'auto', 'amoled')),
    reminder_enabled        BOOLEAN NOT NULL DEFAULT false,
    reminder_time           TEXT NOT NULL DEFAULT '20:00' CHECK (reminder_time ~ '^([01][0-9]|2[0-3]):[0-5][0-9]$'),
    onboarded_at            TIMESTAMPTZ,
    updated_at              TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Per-user ordering of visible categories (system ones included).
CREATE TABLE category_order (
    user_id     UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    category_id UUID NOT NULL REFERENCES categories(id) ON DELETE CASCADE,
    sort_order  INT NOT NULL,
    PRIMARY KEY (user_id, category_id)
);

-- Deleting a user category (DELETE /categories/{id}) nulls it everywhere; recurring
-- expenses were the only reference without ON DELETE SET NULL.
ALTER TABLE recurring_expenses DROP CONSTRAINT IF EXISTS recurring_expenses_category_id_fkey;
ALTER TABLE recurring_expenses ADD CONSTRAINT recurring_expenses_category_id_fkey
    FOREIGN KEY (category_id) REFERENCES categories(id) ON DELETE SET NULL;

-- Unified-ledger links on group data.
ALTER TABLE expenses ADD COLUMN payer_account_id UUID REFERENCES accounts(id) ON DELETE SET NULL;
ALTER TABLE settlements ADD COLUMN from_account_id UUID REFERENCES accounts(id) ON DELETE SET NULL;
CREATE INDEX idx_expenses_payer_account ON expenses (payer_account_id) WHERE payer_account_id IS NOT NULL;
CREATE INDEX idx_settlements_from_account ON settlements (from_account_id) WHERE from_account_id IS NOT NULL;

CREATE TABLE settlement_accounts (
    settlement_id UUID NOT NULL REFERENCES settlements(id) ON DELETE CASCADE,
    user_id       UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    account_id    UUID NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (settlement_id, user_id)
);
CREATE INDEX idx_settlement_accounts_account ON settlement_accounts (account_id);

-- Unified ledger read model. One row per (viewer, entry):
--   account_amount     signed effect on account_id (account currency for transactions,
--                      entry currency for group entries); NULL = no account effect
--   to_account_amount  signed effect on to_account_id (transfers, to-account currency)
--   flow_amount        what counts as income/expense (my share for group expenses,
--                      0 for transfers and settlements)
CREATE VIEW ledger_entries AS
SELECT t.user_id,
       'TRANSACTION'::text AS kind,
       t.id,
       t.type,
       t.account_id,
       t.to_account_id,
       t.amount,
       t.currency,
       t.to_amount,
       ta.currency AS to_currency,
       CASE WHEN t.occurred_at IS NULL OR t.account_id IS NULL THEN NULL::bigint
            WHEN t.type = 'INCOME' THEN t.amount
            ELSE -t.amount END AS account_amount,
       CASE WHEN t.occurred_at IS NOT NULL AND t.type = 'TRANSFER' THEN t.to_amount
            ELSE NULL::bigint END AS to_account_amount,
       CASE WHEN t.type IN ('INCOME', 'EXPENSE') THEN t.amount ELSE 0::bigint END AS flow_amount,
       t.category_id,
       t.title,
       t.description,
       t.occurred_at,
       t.due_at,
       t.source,
       NULL::uuid AS group_id,
       NULL::text AS group_name,
       NULL::uuid AS expense_id,
       NULL::uuid AS settlement_id,
       NULL::bigint AS total_amount,
       COALESCE(t.occurred_at, t.due_at) AS sort_at,
       t.created_at
FROM transactions t
LEFT JOIN accounts ta ON ta.id = t.to_account_id
WHERE t.deleted_at IS NULL
UNION ALL
-- Group expense where I have a split (my share is my expense).
SELECT s.user_id,
       'GROUP_EXPENSE'::text,
       e.id,
       'EXPENSE'::text,
       CASE WHEN e.paid_by = s.user_id THEN e.payer_account_id END,
       NULL::uuid,
       s.amount,
       e.currency,
       NULL::bigint,
       NULL::char(3),
       CASE WHEN e.paid_by = s.user_id AND e.payer_account_id IS NOT NULL THEN -e.amount END,
       NULL::bigint,
       s.amount,
       e.category_id,
       e.description,
       e.notes,
       (e.expense_date::timestamp AT TIME ZONE 'UTC'),
       NULL::timestamptz,
       'GROUP'::text,
       e.group_id,
       g.name,
       e.id,
       NULL::uuid,
       e.amount,
       (e.expense_date::timestamp AT TIME ZONE 'UTC'),
       e.created_at
FROM expense_splits s
JOIN expenses e ON e.id = s.expense_id AND e.deleted_at IS NULL
JOIN groups g ON g.id = e.group_id
WHERE s.amount > 0 OR (e.paid_by = s.user_id AND e.payer_account_id IS NOT NULL)
UNION ALL
-- Group expense I paid from an account without having a share myself.
SELECT e.paid_by,
       'GROUP_EXPENSE'::text,
       e.id,
       'EXPENSE'::text,
       e.payer_account_id,
       NULL::uuid,
       0::bigint,
       e.currency,
       NULL::bigint,
       NULL::char(3),
       -e.amount,
       NULL::bigint,
       0::bigint,
       e.category_id,
       e.description,
       e.notes,
       (e.expense_date::timestamp AT TIME ZONE 'UTC'),
       NULL::timestamptz,
       'GROUP'::text,
       e.group_id,
       g.name,
       e.id,
       NULL::uuid,
       e.amount,
       (e.expense_date::timestamp AT TIME ZONE 'UTC'),
       e.created_at
FROM expenses e
JOIN groups g ON g.id = e.group_id
WHERE e.deleted_at IS NULL AND e.payer_account_id IS NOT NULL
  AND NOT EXISTS (SELECT 1 FROM expense_splits s WHERE s.expense_id = e.id AND s.user_id = e.paid_by)
UNION ALL
-- Settlement I paid from an account.
SELECT st.from_user,
       'SETTLEMENT'::text,
       st.id,
       'SETTLEMENT'::text,
       st.from_account_id,
       NULL::uuid,
       st.amount,
       st.currency,
       NULL::bigint,
       NULL::char(3),
       -st.amount,
       NULL::bigint,
       0::bigint,
       NULL::uuid,
       'Settlement'::text,
       st.note,
       st.settled_at,
       NULL::timestamptz,
       'GROUP'::text,
       st.group_id,
       g.name,
       NULL::uuid,
       st.id,
       st.amount,
       st.settled_at,
       st.created_at
FROM settlements st
JOIN groups g ON g.id = st.group_id
WHERE st.deleted_at IS NULL AND st.from_account_id IS NOT NULL
UNION ALL
-- Settlement I received into an account.
SELECT st.to_user,
       'SETTLEMENT'::text,
       st.id,
       'SETTLEMENT'::text,
       sa.account_id,
       NULL::uuid,
       st.amount,
       st.currency,
       NULL::bigint,
       NULL::char(3),
       st.amount,
       NULL::bigint,
       0::bigint,
       NULL::uuid,
       'Settlement'::text,
       st.note,
       st.settled_at,
       NULL::timestamptz,
       'GROUP'::text,
       st.group_id,
       g.name,
       NULL::uuid,
       st.id,
       st.amount,
       st.settled_at,
       st.created_at
FROM settlements st
JOIN settlement_accounts sa ON sa.settlement_id = st.id AND sa.user_id = st.to_user
JOIN groups g ON g.id = st.group_id
WHERE st.deleted_at IS NULL;

-- Data migration: personal_expenses -> transactions on a default "Cash" account
-- (users.default_currency), plus a "Cash XXX" account per other currency used.
-- Transaction ids reuse the personal expense ids so /personal clients keep working.
INSERT INTO accounts (user_id, name, currency, is_default, sort_order)
SELECT u.id, 'Cash', u.default_currency, true, 0
FROM users u
WHERE EXISTS (SELECT 1 FROM personal_expenses pe WHERE pe.user_id = u.id AND pe.deleted_at IS NULL);

INSERT INTO accounts (user_id, name, currency, is_default, sort_order)
SELECT x.user_id, 'Cash ' || x.currency, x.currency, false,
       row_number() OVER (PARTITION BY x.user_id ORDER BY x.currency)
FROM (
    SELECT DISTINCT pe.user_id, pe.currency
    FROM personal_expenses pe
    JOIN users u ON u.id = pe.user_id
    WHERE pe.deleted_at IS NULL AND pe.currency <> u.default_currency
) x;

INSERT INTO transactions (id, user_id, type, account_id, amount, currency, category_id, title,
                          description, occurred_at, source, created_at, updated_at)
SELECT pe.id, pe.user_id, 'EXPENSE', a.id, pe.amount, pe.currency, pe.category_id, pe.description,
       pe.notes, (pe.expense_date::timestamp AT TIME ZONE 'UTC'), 'PERSONAL_LEGACY', pe.created_at, pe.updated_at
FROM personal_expenses pe
JOIN accounts a ON a.user_id = pe.user_id AND a.currency = pe.currency AND a.deleted_at IS NULL
WHERE pe.deleted_at IS NULL;

-- +goose Down
-- Copy every legacy-API expense (PERSONAL_LEGACY) back so the previous release's
-- /personal endpoints see edits, deletes and new rows. A row that is no longer a
-- live, occurred EXPENSE (deleted, changed type, or turned into a planned item) is
-- marked deleted so a rollback never resurrects stale data. Wallet-only data
-- (MANUAL/IMPORT/ADJUSTMENT transactions, transfers, income, accounts, tags,
-- settings, rate overrides, group account links) is discarded.
INSERT INTO personal_expenses (id, user_id, description, amount, currency, category_id, notes,
                               expense_date, created_at, updated_at, deleted_at)
SELECT t.id, t.user_id, t.title, t.amount, t.currency, t.category_id, t.description,
       (COALESCE(t.occurred_at, t.due_at, t.created_at) AT TIME ZONE 'UTC')::date, t.created_at, t.updated_at,
       COALESCE(t.deleted_at, CASE WHEN t.type <> 'EXPENSE' OR t.occurred_at IS NULL THEN now() END)
FROM transactions t
WHERE t.source = 'PERSONAL_LEGACY'
ON CONFLICT (id) DO UPDATE SET
    description  = EXCLUDED.description,
    amount       = EXCLUDED.amount,
    currency     = EXCLUDED.currency,
    category_id  = EXCLUDED.category_id,
    notes        = EXCLUDED.notes,
    expense_date = EXCLUDED.expense_date,
    updated_at   = EXCLUDED.updated_at,
    deleted_at   = EXCLUDED.deleted_at;

DROP VIEW IF EXISTS ledger_entries;
DROP TABLE IF EXISTS settlement_accounts;
DROP INDEX IF EXISTS idx_settlements_from_account;
DROP INDEX IF EXISTS idx_expenses_payer_account;
ALTER TABLE settlements DROP COLUMN IF EXISTS from_account_id;
ALTER TABLE expenses DROP COLUMN IF EXISTS payer_account_id;
DROP TABLE IF EXISTS category_order;
ALTER TABLE recurring_expenses DROP CONSTRAINT IF EXISTS recurring_expenses_category_id_fkey;
ALTER TABLE recurring_expenses ADD CONSTRAINT recurring_expenses_category_id_fkey
    FOREIGN KEY (category_id) REFERENCES categories(id);
DROP TABLE IF EXISTS user_settings;
DROP TABLE IF EXISTS exchange_rate_overrides;
DROP TABLE IF EXISTS transaction_tags;
DROP TABLE IF EXISTS tags;
DROP TABLE IF EXISTS transactions;
DROP TABLE IF EXISTS accounts;

-- The app mirrors applied versions into the legacy schema_migrations table and
-- backfills goose from it on startup; forget this version there too so a later
-- start re-applies it instead of marking it applied.
-- +goose StatementBegin
DO $$
BEGIN
    IF to_regclass('public.schema_migrations') IS NOT NULL THEN
        DELETE FROM schema_migrations WHERE version = '0013_wallet_foundation';
    END IF;
END $$;
-- +goose StatementEnd
