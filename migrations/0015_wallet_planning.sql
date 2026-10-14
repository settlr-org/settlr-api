-- +goose Up
-- Wallet phase 2 (docs/design/wallet.md): planned payments, budgets, loans.
--
-- Planned payments keep exactly one pending transaction (occurred_at NULL,
-- due_at set, planned_payment_id set) per active plan; resolved_until is the
-- due date of the latest paid/skipped occurrence, from which the next one is
-- generated. Loan transactions (source LOAN) move balances but are never
-- income or expense. The ledger view gains the planned/loan link columns.

CREATE TABLE planned_payments (
    id             UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id        UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    type           TEXT NOT NULL CHECK (type IN ('INCOME', 'EXPENSE', 'TRANSFER')),
    account_id     UUID REFERENCES accounts(id) ON DELETE CASCADE,
    to_account_id  UUID REFERENCES accounts(id) ON DELETE CASCADE,
    amount         BIGINT NOT NULL CHECK (amount > 0),
    currency       CHAR(3) NOT NULL,
    to_amount      BIGINT CHECK (to_amount IS NULL OR to_amount > 0),
    category_id    UUID REFERENCES categories(id) ON DELETE SET NULL,
    title          TEXT NOT NULL DEFAULT '',
    description    TEXT NOT NULL DEFAULT '',
    start_at       TIMESTAMPTZ NOT NULL,
    one_time       BOOLEAN NOT NULL DEFAULT false,
    interval_n     INT CHECK (interval_n IS NULL OR interval_n BETWEEN 1 AND 1000),
    interval_unit  TEXT CHECK (interval_unit IS NULL OR interval_unit IN ('DAY', 'WEEK', 'MONTH', 'YEAR')),
    active         BOOLEAN NOT NULL DEFAULT true,
    resolved_until TIMESTAMPTZ,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at     TIMESTAMPTZ,
    CHECK (one_time OR (interval_n IS NOT NULL AND interval_unit IS NOT NULL)),
    CHECK (type <> 'TRANSFER' OR (account_id IS NOT NULL AND to_account_id IS NOT NULL
                                  AND to_amount IS NOT NULL AND to_account_id <> account_id)),
    CHECK (type = 'TRANSFER' OR (to_account_id IS NULL AND to_amount IS NULL))
);
CREATE INDEX idx_planned_payments_user ON planned_payments (user_id, created_at) WHERE deleted_at IS NULL;
CREATE INDEX idx_planned_payments_account ON planned_payments (account_id);
CREATE INDEX idx_planned_payments_to_account ON planned_payments (to_account_id) WHERE to_account_id IS NOT NULL;

CREATE TABLE planned_payment_tags (
    planned_payment_id UUID NOT NULL REFERENCES planned_payments(id) ON DELETE CASCADE,
    tag_id             UUID NOT NULL REFERENCES tags(id) ON DELETE CASCADE,
    PRIMARY KEY (planned_payment_id, tag_id)
);
CREATE INDEX idx_planned_payment_tags_tag ON planned_payment_tags (tag_id);

CREATE TABLE budgets (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id      UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name         TEXT NOT NULL CHECK (char_length(name) BETWEEN 1 AND 100),
    amount       BIGINT NOT NULL CHECK (amount > 0),
    currency     CHAR(3) NOT NULL,
    -- Empty = every category / every account.
    category_ids UUID[] NOT NULL DEFAULT '{}',
    account_ids  UUID[] NOT NULL DEFAULT '{}',
    sort_order   INT NOT NULL DEFAULT 0,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_budgets_user ON budgets (user_id, sort_order);

CREATE TABLE loans (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id     UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name        TEXT NOT NULL CHECK (char_length(name) BETWEEN 1 AND 100),
    type        TEXT NOT NULL CHECK (type IN ('LEND', 'BORROW')),
    amount      BIGINT NOT NULL CHECK (amount > 0),
    currency    CHAR(3) NOT NULL,
    color       TEXT NOT NULL DEFAULT '#6B7280',
    icon        TEXT NOT NULL DEFAULT 'loan',
    note        TEXT NOT NULL DEFAULT '',
    account_id  UUID REFERENCES accounts(id) ON DELETE SET NULL,
    occurred_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    sort_order  INT NOT NULL DEFAULT 0,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at  TIMESTAMPTZ
);
CREATE INDEX idx_loans_user ON loans (user_id, sort_order) WHERE deleted_at IS NULL;
CREATE INDEX idx_loans_account ON loans (account_id) WHERE account_id IS NOT NULL;

CREATE TABLE loan_records (
    id             UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    loan_id        UUID NOT NULL REFERENCES loans(id) ON DELETE CASCADE,
    user_id        UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    type           TEXT NOT NULL CHECK (type IN ('INCREASE', 'DECREASE')),
    amount         BIGINT NOT NULL CHECK (amount > 0),
    note           TEXT NOT NULL DEFAULT '',
    occurred_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    account_id     UUID REFERENCES accounts(id) ON DELETE SET NULL,
    transaction_id UUID REFERENCES transactions(id) ON DELETE SET NULL,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at     TIMESTAMPTZ
);
CREATE INDEX idx_loan_records_loan ON loan_records (loan_id, occurred_at DESC) WHERE deleted_at IS NULL;
CREATE INDEX idx_loan_records_account ON loan_records (account_id) WHERE account_id IS NOT NULL;

-- Phase 1 left these link columns without FKs until the tables existed.
UPDATE transactions SET planned_payment_id = NULL WHERE planned_payment_id IS NOT NULL;
UPDATE transactions SET loan_id = NULL, loan_record_id = NULL WHERE loan_id IS NOT NULL OR loan_record_id IS NOT NULL;
ALTER TABLE transactions ADD CONSTRAINT transactions_planned_payment_id_fkey
    FOREIGN KEY (planned_payment_id) REFERENCES planned_payments(id) ON DELETE SET NULL;
ALTER TABLE transactions ADD CONSTRAINT transactions_loan_id_fkey
    FOREIGN KEY (loan_id) REFERENCES loans(id) ON DELETE SET NULL;
ALTER TABLE transactions ADD CONSTRAINT transactions_loan_record_id_fkey
    FOREIGN KEY (loan_record_id) REFERENCES loan_records(id) ON DELETE SET NULL;
-- At most one pending occurrence per plan.
CREATE UNIQUE INDEX idx_transactions_plan_pending ON transactions (planned_payment_id)
    WHERE planned_payment_id IS NOT NULL AND occurred_at IS NULL AND deleted_at IS NULL;

-- Ledger view: same rows as 0014 plus planned_payment_id, loan_id, loan_record_id.
CREATE OR REPLACE VIEW ledger_entries AS
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
       t.created_at,
       t.planned_payment_id,
       t.loan_id,
       t.loan_record_id
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
       CASE WHEN e.created_at >= (e.expense_date::timestamp AT TIME ZONE 'UTC')
             AND e.created_at < ((e.expense_date + 1)::timestamp AT TIME ZONE 'UTC')
            THEN e.created_at
            ELSE (e.expense_date::timestamp AT TIME ZONE 'UTC') END,
       e.created_at,
       NULL::uuid,
       NULL::uuid,
       NULL::uuid
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
       CASE WHEN e.created_at >= (e.expense_date::timestamp AT TIME ZONE 'UTC')
             AND e.created_at < ((e.expense_date + 1)::timestamp AT TIME ZONE 'UTC')
            THEN e.created_at
            ELSE (e.expense_date::timestamp AT TIME ZONE 'UTC') END,
       e.created_at,
       NULL::uuid,
       NULL::uuid,
       NULL::uuid
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
       st.created_at,
       NULL::uuid,
       NULL::uuid,
       NULL::uuid
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
       st.created_at,
       NULL::uuid,
       NULL::uuid,
       NULL::uuid
FROM settlements st
JOIN settlement_accounts sa ON sa.settlement_id = st.id AND sa.user_id = st.to_user
JOIN groups g ON g.id = st.group_id
WHERE st.deleted_at IS NULL;

-- +goose Down
-- Loan bookings keep their balance effect but become adjustments (never income
-- or expense) so the previous release does not count them as cash flow.
UPDATE transactions SET source = 'ADJUSTMENT' WHERE source = 'LOAN';
DROP VIEW IF EXISTS ledger_entries;
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
       CASE WHEN e.created_at >= (e.expense_date::timestamp AT TIME ZONE 'UTC')
             AND e.created_at < ((e.expense_date + 1)::timestamp AT TIME ZONE 'UTC')
            THEN e.created_at
            ELSE (e.expense_date::timestamp AT TIME ZONE 'UTC') END,
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
       CASE WHEN e.created_at >= (e.expense_date::timestamp AT TIME ZONE 'UTC')
             AND e.created_at < ((e.expense_date + 1)::timestamp AT TIME ZONE 'UTC')
            THEN e.created_at
            ELSE (e.expense_date::timestamp AT TIME ZONE 'UTC') END,
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

DROP INDEX IF EXISTS idx_transactions_plan_pending;
ALTER TABLE transactions DROP CONSTRAINT IF EXISTS transactions_loan_record_id_fkey;
ALTER TABLE transactions DROP CONSTRAINT IF EXISTS transactions_loan_id_fkey;
ALTER TABLE transactions DROP CONSTRAINT IF EXISTS transactions_planned_payment_id_fkey;
UPDATE transactions SET planned_payment_id = NULL, loan_id = NULL, loan_record_id = NULL
WHERE planned_payment_id IS NOT NULL OR loan_id IS NOT NULL OR loan_record_id IS NOT NULL;
DROP TABLE IF EXISTS loan_records;
DROP TABLE IF EXISTS loans;
DROP TABLE IF EXISTS budgets;
DROP TABLE IF EXISTS planned_payment_tags;
DROP TABLE IF EXISTS planned_payments;

-- +goose StatementBegin
DO $$
BEGIN
    IF to_regclass('public.schema_migrations') IS NOT NULL THEN
        DELETE FROM schema_migrations WHERE version = '0015_wallet_planning';
    END IF;
END $$;
-- +goose StatementEnd
