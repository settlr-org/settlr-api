-- +goose Up
-- Group entries stay in a former member's ledger (their own financial history and the account
-- balances depend on them) but the free text other people wrote is redacted once the user is no
-- longer a member of the group: expense notes and settlement notes come back as ''. Titles (the
-- expense description), amounts and the group name stay. There is no removal timestamp, so
-- expenses created or edited after the user left cannot be told apart and are treated alike.
-- Same columns, names and order as 0015.
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
       CASE WHEN EXISTS (SELECT 1 FROM group_members gm WHERE gm.group_id = e.group_id AND gm.user_id = s.user_id) THEN e.notes ELSE ''::text END,
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
       CASE WHEN EXISTS (SELECT 1 FROM group_members gm WHERE gm.group_id = e.group_id AND gm.user_id = e.paid_by) THEN e.notes ELSE ''::text END,
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
       CASE WHEN EXISTS (SELECT 1 FROM group_members gm WHERE gm.group_id = st.group_id AND gm.user_id = st.from_user) THEN st.note ELSE ''::text END,
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
       CASE WHEN EXISTS (SELECT 1 FROM group_members gm WHERE gm.group_id = st.group_id AND gm.user_id = st.to_user) THEN st.note ELSE ''::text END,
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
