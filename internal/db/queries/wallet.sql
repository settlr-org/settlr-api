-- Wallet (phase 1) queries: accounts, transactions, tags, settings, rate overrides
-- and the unified ledger view. Handlers live in internal/wallet.

-- ---------------------------------------------------------------- accounts

-- name: ListAccounts :many
SELECT * FROM accounts
WHERE user_id = $1 AND deleted_at IS NULL
ORDER BY sort_order, created_at, id;

-- name: GetAccount :one
SELECT * FROM accounts WHERE id = $1 AND user_id = $2 AND deleted_at IS NULL;

-- name: GetDefaultAccount :one
SELECT * FROM accounts WHERE user_id = $1 AND is_default AND deleted_at IS NULL;

-- name: GetFirstAccountByCurrency :one
SELECT * FROM accounts
WHERE user_id = $1 AND currency = $2 AND deleted_at IS NULL
ORDER BY archived_at IS NOT NULL, sort_order, created_at
LIMIT 1;

-- name: NextAccountSortOrder :one
SELECT COALESCE(MAX(sort_order) + 1, 0)::int FROM accounts WHERE user_id = $1 AND deleted_at IS NULL;

-- name: CreateAccount :one
INSERT INTO accounts (user_id, name, currency, color, icon, include_in_balance, is_default, sort_order, archived_at)
VALUES (@user_id, @name, @currency, @color, @icon, @include_in_balance, @is_default, @sort_order, @archived_at)
RETURNING *;

-- name: UpdateAccount :one
UPDATE accounts SET
    name = @name, currency = @currency, color = @color, icon = @icon,
    include_in_balance = @include_in_balance, archived_at = @archived_at, updated_at = now()
WHERE id = @id AND user_id = @user_id AND deleted_at IS NULL
RETURNING *;

-- name: ClearDefaultAccount :exec
UPDATE accounts SET is_default = false, updated_at = now() WHERE user_id = $1 AND is_default;

-- name: MarkDefaultAccount :exec
UPDATE accounts SET is_default = true, updated_at = now() WHERE id = $1 AND user_id = $2 AND deleted_at IS NULL;

-- name: PromoteDefaultAccount :exec
-- After the default account is deleted, the first remaining account becomes the default.
UPDATE accounts SET is_default = true, updated_at = now()
WHERE id = (
    SELECT a.id FROM accounts a
    WHERE a.user_id = $1 AND a.deleted_at IS NULL
    ORDER BY a.archived_at IS NOT NULL, a.sort_order, a.created_at
    LIMIT 1
) AND NOT EXISTS (SELECT 1 FROM accounts d WHERE d.user_id = $1 AND d.is_default AND d.deleted_at IS NULL);

-- name: SetAccountSortOrder :execrows
UPDATE accounts SET sort_order = $3, updated_at = now() WHERE id = $1 AND user_id = $2 AND deleted_at IS NULL;

-- name: SoftDeleteAccount :execrows
UPDATE accounts SET deleted_at = now(), is_default = false, updated_at = now()
WHERE id = $1 AND user_id = $2 AND deleted_at IS NULL;

-- name: SoftDeleteAccountTransactions :exec
UPDATE transactions SET deleted_at = now(), updated_at = now()
WHERE user_id = @user_id AND deleted_at IS NULL
  AND (account_id = @account_id::uuid OR to_account_id = @account_id::uuid);

-- name: ClearExpensePayerAccount :exec
UPDATE expenses SET payer_account_id = NULL WHERE payer_account_id = $1;

-- name: ClearSettlementFromAccount :exec
UPDATE settlements SET from_account_id = NULL WHERE from_account_id = $1;

-- name: DeleteSettlementAccountsByAccount :exec
DELETE FROM settlement_accounts WHERE account_id = $1;

-- name: AccountHasActivity :one
SELECT (
    EXISTS (SELECT 1 FROM transactions t WHERE (t.account_id = @account_id::uuid OR t.to_account_id = @account_id::uuid) AND t.deleted_at IS NULL)
 OR EXISTS (SELECT 1 FROM expenses e WHERE e.payer_account_id = @account_id::uuid AND e.deleted_at IS NULL)
 OR EXISTS (SELECT 1 FROM settlements s WHERE s.from_account_id = @account_id::uuid AND s.deleted_at IS NULL)
 OR EXISTS (SELECT 1 FROM settlement_accounts sa WHERE sa.account_id = @account_id::uuid)
)::bool AS has_activity;

-- name: AccountBalances :many
-- Sum of account effects per (account, currency) over live, non-planned entries.
-- Group entries may be in a currency other than the account's; callers convert.
SELECT m.account_id::uuid AS account_id, m.currency::text AS currency, SUM(m.delta)::bigint AS total
FROM (
    SELECT le.account_id, le.currency, le.account_amount AS delta
    FROM ledger_entries le
    WHERE le.user_id = @user_id AND le.account_id IS NOT NULL AND le.account_amount IS NOT NULL
      AND (sqlc.narg(until)::timestamptz IS NULL OR le.occurred_at < sqlc.narg(until)::timestamptz)
    UNION ALL
    SELECT le.to_account_id, le.to_currency, le.to_account_amount
    FROM ledger_entries le
    WHERE le.user_id = @user_id AND le.to_account_id IS NOT NULL AND le.to_account_amount IS NOT NULL
      AND (sqlc.narg(until)::timestamptz IS NULL OR le.occurred_at < sqlc.narg(until)::timestamptz)
) m
JOIN accounts a ON a.id = m.account_id AND a.user_id = @user_id AND a.deleted_at IS NULL
GROUP BY m.account_id, m.currency;

-- name: AccountMovements :many
-- In/out totals for one account within [from_at, to_at), per currency, plus the
-- part that came from INCOME/EXPENSE transactions and from transfers. Balance
-- adjustments (starting balances, corrections) move the account but are neither
-- income nor expense.
SELECT m.currency::text AS currency,
       COALESCE(SUM(m.delta) FILTER (WHERE m.delta > 0), 0)::bigint AS inflow,
       COALESCE(-SUM(m.delta) FILTER (WHERE m.delta < 0), 0)::bigint AS outflow,
       COALESCE(SUM(m.delta) FILTER (WHERE m.kind = 'TRANSACTION' AND m.type = 'INCOME' AND m.source <> 'ADJUSTMENT'), 0)::bigint AS income,
       COALESCE(-SUM(m.delta) FILTER (WHERE m.kind = 'TRANSACTION' AND m.type = 'EXPENSE' AND m.source <> 'ADJUSTMENT'), 0)::bigint AS expense,
       COALESCE(SUM(m.delta) FILTER (WHERE m.kind = 'TRANSACTION' AND m.type = 'TRANSFER' AND m.delta > 0), 0)::bigint AS transfer_in,
       COALESCE(-SUM(m.delta) FILTER (WHERE m.kind = 'TRANSACTION' AND m.type = 'TRANSFER' AND m.delta < 0), 0)::bigint AS transfer_out
FROM (
    SELECT le.kind, le.type, le.source, le.currency, le.account_amount AS delta, le.occurred_at
    FROM ledger_entries le
    WHERE le.user_id = @user_id AND le.account_id = @account_id::uuid AND le.account_amount IS NOT NULL
    UNION ALL
    SELECT le.kind, le.type, le.source, le.to_currency, le.to_account_amount, le.occurred_at
    FROM ledger_entries le
    WHERE le.user_id = @user_id AND le.to_account_id = @account_id::uuid AND le.to_account_amount IS NOT NULL
) m
WHERE (sqlc.narg(from_at)::timestamptz IS NULL OR m.occurred_at >= sqlc.narg(from_at)::timestamptz)
  AND (sqlc.narg(to_at)::timestamptz IS NULL OR m.occurred_at < sqlc.narg(to_at)::timestamptz)
GROUP BY m.currency;

-- ---------------------------------------------------------------- categories & tags

-- name: CategoryVisible :one
SELECT EXISTS (SELECT 1 FROM categories WHERE id = @id::uuid AND (is_system OR owner_id = @user_id::uuid))::bool;

-- name: CountOwnedTags :one
SELECT COUNT(*)::int FROM tags WHERE user_id = @user_id AND id = ANY(@ids::uuid[]);

-- name: ListTags :many
SELECT * FROM tags WHERE user_id = $1 ORDER BY sort_order, lower(name), id;

-- name: GetTag :one
SELECT * FROM tags WHERE id = $1 AND user_id = $2;

-- name: NextTagSortOrder :one
SELECT COALESCE(MAX(sort_order) + 1, 0)::int FROM tags WHERE user_id = $1;

-- name: CreateTag :one
INSERT INTO tags (user_id, name, color, icon, sort_order)
VALUES (@user_id, @name, @color, @icon, @sort_order)
RETURNING *;

-- name: UpdateTag :one
UPDATE tags SET name = @name, color = @color, icon = @icon, sort_order = @sort_order, updated_at = now()
WHERE id = @id AND user_id = @user_id
RETURNING *;

-- name: DeleteTag :execrows
DELETE FROM tags WHERE id = $1 AND user_id = $2;

-- name: ListTagsForTransactions :many
SELECT tt.transaction_id, t.id, t.name, t.color, t.icon
FROM transaction_tags tt
JOIN tags t ON t.id = tt.tag_id
WHERE tt.transaction_id = ANY(@transaction_ids::uuid[])
ORDER BY t.sort_order, lower(t.name);

-- name: DeleteTransactionTags :exec
DELETE FROM transaction_tags WHERE transaction_id = $1;

-- name: AddTransactionTags :exec
INSERT INTO transaction_tags (transaction_id, tag_id)
SELECT @transaction_id::uuid, unnest(@tag_ids::uuid[])
ON CONFLICT DO NOTHING;

-- ---------------------------------------------------------------- transactions

-- name: CreateTransaction :one
INSERT INTO transactions (
    id, user_id, type, account_id, to_account_id, amount, currency, to_amount, category_id,
    title, description, occurred_at, due_at, planned_payment_id, loan_id, loan_record_id, source, import_ref
) VALUES (
    @id, @user_id, @type, sqlc.narg(account_id), sqlc.narg(to_account_id), @amount, @currency, sqlc.narg(to_amount),
    NULLIF(@category_id::uuid, '00000000-0000-0000-0000-000000000000'::uuid),
    @title, @description, sqlc.narg(occurred_at), sqlc.narg(due_at), sqlc.narg(planned_payment_id),
    sqlc.narg(loan_id), sqlc.narg(loan_record_id), @source, sqlc.narg(import_ref)
)
RETURNING *;

-- name: GetTransaction :one
SELECT * FROM transactions WHERE id = $1 AND user_id = $2 AND deleted_at IS NULL;

-- name: UpdateTransaction :one
UPDATE transactions SET
    type = @type, account_id = sqlc.narg(account_id), to_account_id = sqlc.narg(to_account_id),
    amount = @amount, currency = @currency, to_amount = sqlc.narg(to_amount),
    category_id = NULLIF(@category_id::uuid, '00000000-0000-0000-0000-000000000000'::uuid),
    title = @title, description = @description, occurred_at = sqlc.narg(occurred_at), due_at = sqlc.narg(due_at),
    updated_at = now()
WHERE id = @id AND user_id = @user_id AND deleted_at IS NULL
RETURNING *;

-- name: SoftDeleteTransaction :execrows
UPDATE transactions SET deleted_at = now(), updated_at = now()
WHERE id = $1 AND user_id = $2 AND deleted_at IS NULL;

-- name: ListTransactions :many
SELECT t.* FROM transactions t
WHERE t.user_id = @user_id AND t.deleted_at IS NULL
  AND (CASE @planned::text WHEN 'only' THEN t.occurred_at IS NULL WHEN 'all' THEN true ELSE t.occurred_at IS NOT NULL END)
  AND (sqlc.narg(from_at)::timestamptz IS NULL OR COALESCE(t.occurred_at, t.due_at) >= sqlc.narg(from_at)::timestamptz)
  AND (sqlc.narg(to_at)::timestamptz IS NULL OR COALESCE(t.occurred_at, t.due_at) < sqlc.narg(to_at)::timestamptz)
  AND (cardinality(@types::text[]) = 0 OR t.type = ANY(@types::text[]))
  AND (cardinality(@account_ids::uuid[]) = 0 OR t.account_id = ANY(@account_ids::uuid[]) OR t.to_account_id = ANY(@account_ids::uuid[]))
  AND (cardinality(@category_ids::uuid[]) = 0 OR t.category_id = ANY(@category_ids::uuid[]))
  AND (cardinality(@tag_ids::uuid[]) = 0 OR EXISTS (
        SELECT 1 FROM transaction_tags tt WHERE tt.transaction_id = t.id AND tt.tag_id = ANY(@tag_ids::uuid[])))
  AND (cardinality(@excl_tag_ids::uuid[]) = 0 OR NOT EXISTS (
        SELECT 1 FROM transaction_tags tt WHERE tt.transaction_id = t.id AND tt.tag_id = ANY(@excl_tag_ids::uuid[])))
  AND (@q::text = '' OR t.title ILIKE '%' || @q::text || '%' OR t.description ILIKE '%' || @q::text || '%')
  AND (@excl_q::text = '' OR NOT (t.title ILIKE '%' || @excl_q::text || '%' OR t.description ILIKE '%' || @excl_q::text || '%'))
  AND (sqlc.narg(min_amount)::bigint IS NULL OR t.amount >= sqlc.narg(min_amount)::bigint)
  AND (sqlc.narg(max_amount)::bigint IS NULL OR t.amount <= sqlc.narg(max_amount)::bigint)
  AND (sqlc.narg(cursor_at)::timestamptz IS NULL
       OR (COALESCE(t.occurred_at, t.due_at), t.id) < (sqlc.narg(cursor_at)::timestamptz, @cursor_id::uuid))
ORDER BY COALESCE(t.occurred_at, t.due_at) DESC, t.id DESC
LIMIT @lim::int;

-- ---------------------------------------------------------------- ledger

-- name: ListLedgerEntries :many
SELECT le.* FROM ledger_entries le
WHERE le.user_id = @user_id
  AND (CASE @planned::text WHEN 'only' THEN le.occurred_at IS NULL WHEN 'all' THEN true ELSE le.occurred_at IS NOT NULL END)
  AND (sqlc.narg(from_at)::timestamptz IS NULL OR le.sort_at >= sqlc.narg(from_at)::timestamptz)
  AND (sqlc.narg(to_at)::timestamptz IS NULL OR le.sort_at < sqlc.narg(to_at)::timestamptz)
  AND (cardinality(@kinds::text[]) = 0 OR le.kind = ANY(@kinds::text[]))
  AND (cardinality(@types::text[]) = 0 OR le.type = ANY(@types::text[]))
  AND (cardinality(@account_ids::uuid[]) = 0 OR le.account_id = ANY(@account_ids::uuid[]) OR le.to_account_id = ANY(@account_ids::uuid[]))
  AND (cardinality(@category_ids::uuid[]) = 0 OR le.category_id = ANY(@category_ids::uuid[]))
  AND (cardinality(@group_ids::uuid[]) = 0 OR le.group_id = ANY(@group_ids::uuid[]))
  AND (cardinality(@tag_ids::uuid[]) = 0 OR (le.kind = 'TRANSACTION' AND EXISTS (
        SELECT 1 FROM transaction_tags tt WHERE tt.transaction_id = le.id AND tt.tag_id = ANY(@tag_ids::uuid[]))))
  AND (cardinality(@excl_tag_ids::uuid[]) = 0 OR le.kind <> 'TRANSACTION' OR NOT EXISTS (
        SELECT 1 FROM transaction_tags tt WHERE tt.transaction_id = le.id AND tt.tag_id = ANY(@excl_tag_ids::uuid[])))
  AND (@q::text = '' OR le.title ILIKE '%' || @q::text || '%' OR le.description ILIKE '%' || @q::text || '%')
  AND (@excl_q::text = '' OR NOT (le.title ILIKE '%' || @excl_q::text || '%' OR le.description ILIKE '%' || @excl_q::text || '%'))
  AND (sqlc.narg(min_amount)::bigint IS NULL OR le.amount >= sqlc.narg(min_amount)::bigint)
  AND (sqlc.narg(max_amount)::bigint IS NULL OR le.amount <= sqlc.narg(max_amount)::bigint)
  AND (sqlc.narg(cursor_at)::timestamptz IS NULL
       OR (le.sort_at, le.id) < (sqlc.narg(cursor_at)::timestamptz, @cursor_id::uuid))
ORDER BY le.sort_at DESC, le.id DESC
LIMIT @lim::int;

-- name: LedgerFlowTotals :many
-- Income/expense flows within [from_at, to_at) per currency. Transactions on accounts
-- excluded from the balance are left out; group shares always count. Balance
-- adjustments (starting balances, corrections) are not income or expense.
SELECT le.currency::text AS currency,
       COALESCE(SUM(le.flow_amount) FILTER (WHERE le.type = 'INCOME'), 0)::bigint AS income,
       COALESCE(SUM(le.flow_amount) FILTER (WHERE le.type = 'EXPENSE'), 0)::bigint AS expense,
       COUNT(*)::int AS entries
FROM ledger_entries le
LEFT JOIN accounts a ON a.id = le.account_id
WHERE le.user_id = @user_id
  AND le.occurred_at IS NOT NULL
  AND le.type IN ('INCOME', 'EXPENSE')
  AND le.source <> 'ADJUSTMENT'
  AND (le.kind <> 'TRANSACTION' OR a.include_in_balance)
  AND (sqlc.narg(from_at)::timestamptz IS NULL OR le.occurred_at >= sqlc.narg(from_at)::timestamptz)
  AND (sqlc.narg(to_at)::timestamptz IS NULL OR le.occurred_at < sqlc.narg(to_at)::timestamptz)
  AND (sqlc.narg(category_id)::uuid IS NULL OR le.category_id = sqlc.narg(category_id)::uuid)
GROUP BY le.currency;

-- ---------------------------------------------------------------- settings & rates

-- name: GetUserSettings :one
SELECT * FROM user_settings WHERE user_id = $1;

-- name: UpsertUserSettings :one
INSERT INTO user_settings (user_id, month_start_day, hide_balance, hide_income, treat_transfers_as_flow,
                           theme, reminder_enabled, reminder_time, onboarded_at, updated_at)
VALUES (@user_id, @month_start_day, @hide_balance, @hide_income, @treat_transfers_as_flow,
        @theme, @reminder_enabled, @reminder_time, @onboarded_at, now())
ON CONFLICT (user_id) DO UPDATE SET
    month_start_day = EXCLUDED.month_start_day,
    hide_balance = EXCLUDED.hide_balance,
    hide_income = EXCLUDED.hide_income,
    treat_transfers_as_flow = EXCLUDED.treat_transfers_as_flow,
    theme = EXCLUDED.theme,
    reminder_enabled = EXCLUDED.reminder_enabled,
    reminder_time = EXCLUDED.reminder_time,
    onboarded_at = EXCLUDED.onboarded_at,
    updated_at = now()
RETURNING *;

-- name: ListRateOverrides :many
SELECT base::text AS base, quote::text AS quote, rate::float8 AS rate, updated_at
FROM exchange_rate_overrides WHERE user_id = $1 ORDER BY base, quote;

-- name: UpsertRateOverride :one
INSERT INTO exchange_rate_overrides (user_id, base, quote, rate, updated_at)
VALUES (@user_id, @base, @quote, @rate::float8, now())
ON CONFLICT (user_id, base, quote) DO UPDATE SET rate = EXCLUDED.rate, updated_at = now()
RETURNING base::text AS base, quote::text AS quote, rate::float8 AS rate, updated_at;

-- name: DeleteRateOverride :execrows
DELETE FROM exchange_rate_overrides WHERE user_id = $1 AND base = $2 AND quote = $3;

-- ---------------------------------------------------------------- group links

-- name: SetExpensePayerAccount :exec
UPDATE expenses SET payer_account_id = sqlc.narg(payer_account_id) WHERE id = @id;

-- name: GetExpensePayerAccount :one
SELECT paid_by, payer_account_id FROM expenses WHERE id = $1;

-- name: GetSettlementParties :one
SELECT group_id, from_user, to_user, from_account_id FROM settlements WHERE id = $1 AND deleted_at IS NULL;

-- name: SetSettlementFromAccount :exec
UPDATE settlements SET from_account_id = sqlc.narg(from_account_id), updated_at = now() WHERE id = @id;

-- name: UpsertSettlementAccount :exec
INSERT INTO settlement_accounts (settlement_id, user_id, account_id) VALUES ($1, $2, $3)
ON CONFLICT (settlement_id, user_id) DO UPDATE SET account_id = EXCLUDED.account_id, created_at = now();

-- name: DeleteSettlementAccount :exec
DELETE FROM settlement_accounts WHERE settlement_id = $1 AND user_id = $2;

-- name: GetSettlementAccount :one
SELECT account_id FROM settlement_accounts WHERE settlement_id = $1 AND user_id = $2;

-- name: LockUserWallet :exec
-- Serialises account bootstrapping (default "Cash" / "Cash XXX") per user for the
-- rest of the current transaction.
SELECT pg_advisory_xact_lock(hashtext('wallet-accounts:' || @user_id::text));
