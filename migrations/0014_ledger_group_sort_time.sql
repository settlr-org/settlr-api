-- +goose Up
-- Group expenses carry only a date, so their ledger sort_at was midnight UTC and
-- they always sorted below wallet transactions of the same day even when added
-- later. When an expense was created on its own expense_date (UTC), sort it at
-- its creation time instead; other (back/forward-dated) expenses keep midnight.
-- sort_at stays within the expense_date day, so date-range filters and the
-- (sort_at, id) ledger cursor are unaffected. occurred_at is unchanged.
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

-- +goose Down
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

-- +goose StatementBegin
DO $$
BEGIN
    IF to_regclass('public.schema_migrations') IS NOT NULL THEN
        DELETE FROM schema_migrations WHERE version = '0014_ledger_group_sort_time';
    END IF;
END $$;
-- +goose StatementEnd
