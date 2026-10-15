-- NOTE: the filter block below is identical to ListLedgerEntries
-- (wallet.sql); keep them in sync (the ledger/report consistency tests catch drift).
-- Reports aggregate in SQL per currency; Go converts per currency pair.

-- name: ReportTotals :many
-- Income/expense (paid or, when `planned` says so, planned entries), transfer legs and entry counts per
-- currency. A transfer has two legs: out (leg 0, amount in the source currency) and in (leg 1, to_amount in
-- the target currency); with an account filter only the legs on the selected accounts count.
SELECT (CASE WHEN leg.n = 0 THEN le.currency ELSE COALESCE(le.to_currency, le.currency) END)::text AS currency,
       COALESCE(SUM(le.flow_amount) FILTER (WHERE leg.n = 0 AND le.type = 'INCOME'), 0)::bigint AS income,
       COALESCE(SUM(le.flow_amount) FILTER (WHERE leg.n = 0 AND le.type = 'EXPENSE'), 0)::bigint AS expense,
       COUNT(*) FILTER (WHERE leg.n = 0 AND le.type IN ('INCOME', 'EXPENSE'))::int AS entries,
       COALESCE(SUM(le.amount) FILTER (WHERE leg.n = 0 AND le.type = 'TRANSFER' AND (cardinality(@account_ids::uuid[]) = 0 OR le.account_id = ANY(@account_ids::uuid[]))), 0)::bigint AS transfers_out,
       COALESCE(SUM(COALESCE(le.to_amount, le.amount)) FILTER (WHERE leg.n = 1 AND (cardinality(@account_ids::uuid[]) = 0 OR le.to_account_id = ANY(@account_ids::uuid[]))), 0)::bigint AS transfers_in,
       COUNT(*) FILTER (WHERE leg.n = 0 AND le.type = 'TRANSFER')::int AS transfer_entries
FROM ledger_entries le
LEFT JOIN accounts a ON a.id = le.account_id
CROSS JOIN LATERAL (VALUES (0), (1)) AS leg(n)
WHERE le.user_id = @user_id
  AND (CASE @planned::text WHEN 'only' THEN le.occurred_at IS NULL WHEN 'all' THEN true ELSE le.occurred_at IS NOT NULL END)
  AND (sqlc.narg(from_at)::timestamptz IS NULL OR COALESCE(le.occurred_at, le.due_at) >= sqlc.narg(from_at)::timestamptz)
  AND (sqlc.narg(to_at)::timestamptz IS NULL OR COALESCE(le.occurred_at, le.due_at) < sqlc.narg(to_at)::timestamptz)
  AND (cardinality(@kinds::text[]) = 0 OR le.kind = ANY(@kinds::text[]))
  AND (cardinality(@types::text[]) = 0 OR le.type = ANY(@types::text[]))
  AND (cardinality(@sources::text[]) = 0 OR le.source = ANY(@sources::text[]))
  AND (cardinality(@excl_sources::text[]) = 0 OR NOT (le.source = ANY(@excl_sources::text[])))
  AND (cardinality(@account_ids::uuid[]) = 0 OR le.account_id = ANY(@account_ids::uuid[]) OR le.to_account_id = ANY(@account_ids::uuid[]))
  AND ((cardinality(@category_ids::uuid[]) = 0 AND NOT @category_none::bool)
       OR le.category_id = ANY(@category_ids::uuid[])
       OR (@category_none::bool AND le.category_id IS NULL))
  AND (cardinality(@group_ids::uuid[]) = 0 OR le.group_id = ANY(@group_ids::uuid[]))
  AND (cardinality(@tag_ids::uuid[]) = 0 OR (le.kind = 'TRANSACTION' AND EXISTS (
        SELECT 1 FROM transaction_tags tt WHERE tt.transaction_id = le.id AND tt.tag_id = ANY(@tag_ids::uuid[]))))
  AND (cardinality(@excl_tag_ids::uuid[]) = 0 OR le.kind <> 'TRANSACTION' OR NOT EXISTS (
        SELECT 1 FROM transaction_tags tt WHERE tt.transaction_id = le.id AND tt.tag_id = ANY(@excl_tag_ids::uuid[])))
  AND (cardinality(@q_terms::text[]) < 1 OR le.title ILIKE (@q_terms::text[])[1] OR le.description ILIKE (@q_terms::text[])[1] OR le.id = ANY(ARRAY(SELECT tt.transaction_id FROM transaction_tags tt JOIN tags g ON g.id = tt.tag_id WHERE g.user_id = @user_id AND g.name ILIKE (@q_terms::text[])[1])))
  AND (cardinality(@q_terms::text[]) < 2 OR le.title ILIKE (@q_terms::text[])[2] OR le.description ILIKE (@q_terms::text[])[2] OR le.id = ANY(ARRAY(SELECT tt.transaction_id FROM transaction_tags tt JOIN tags g ON g.id = tt.tag_id WHERE g.user_id = @user_id AND g.name ILIKE (@q_terms::text[])[2])))
  AND (cardinality(@q_terms::text[]) < 3 OR le.title ILIKE (@q_terms::text[])[3] OR le.description ILIKE (@q_terms::text[])[3] OR le.id = ANY(ARRAY(SELECT tt.transaction_id FROM transaction_tags tt JOIN tags g ON g.id = tt.tag_id WHERE g.user_id = @user_id AND g.name ILIKE (@q_terms::text[])[3])))
  AND (cardinality(@q_terms::text[]) < 4 OR le.title ILIKE (@q_terms::text[])[4] OR le.description ILIKE (@q_terms::text[])[4] OR le.id = ANY(ARRAY(SELECT tt.transaction_id FROM transaction_tags tt JOIN tags g ON g.id = tt.tag_id WHERE g.user_id = @user_id AND g.name ILIKE (@q_terms::text[])[4])))
  AND (cardinality(@q_terms::text[]) < 5 OR le.title ILIKE (@q_terms::text[])[5] OR le.description ILIKE (@q_terms::text[])[5] OR le.id = ANY(ARRAY(SELECT tt.transaction_id FROM transaction_tags tt JOIN tags g ON g.id = tt.tag_id WHERE g.user_id = @user_id AND g.name ILIKE (@q_terms::text[])[5])))
  AND (cardinality(@q_terms::text[]) < 6 OR le.title ILIKE (@q_terms::text[])[6] OR le.description ILIKE (@q_terms::text[])[6] OR le.id = ANY(ARRAY(SELECT tt.transaction_id FROM transaction_tags tt JOIN tags g ON g.id = tt.tag_id WHERE g.user_id = @user_id AND g.name ILIKE (@q_terms::text[])[6])))
  AND (cardinality(@q_terms::text[]) < 7 OR le.title ILIKE (@q_terms::text[])[7] OR le.description ILIKE (@q_terms::text[])[7] OR le.id = ANY(ARRAY(SELECT tt.transaction_id FROM transaction_tags tt JOIN tags g ON g.id = tt.tag_id WHERE g.user_id = @user_id AND g.name ILIKE (@q_terms::text[])[7])))
  AND (cardinality(@q_terms::text[]) < 8 OR le.title ILIKE (@q_terms::text[])[8] OR le.description ILIKE (@q_terms::text[])[8] OR le.id = ANY(ARRAY(SELECT tt.transaction_id FROM transaction_tags tt JOIN tags g ON g.id = tt.tag_id WHERE g.user_id = @user_id AND g.name ILIKE (@q_terms::text[])[8])))
  AND (cardinality(@q_terms::text[]) < 9 OR le.title ILIKE (@q_terms::text[])[9] OR le.description ILIKE (@q_terms::text[])[9] OR le.id = ANY(ARRAY(SELECT tt.transaction_id FROM transaction_tags tt JOIN tags g ON g.id = tt.tag_id WHERE g.user_id = @user_id AND g.name ILIKE (@q_terms::text[])[9])))
  AND (cardinality(@q_terms::text[]) < 10 OR le.title ILIKE (@q_terms::text[])[10] OR le.description ILIKE (@q_terms::text[])[10] OR le.id = ANY(ARRAY(SELECT tt.transaction_id FROM transaction_tags tt JOIN tags g ON g.id = tt.tag_id WHERE g.user_id = @user_id AND g.name ILIKE (@q_terms::text[])[10])))
  AND (cardinality(@excl_q_terms::text[]) = 0 OR NOT (le.title ILIKE ANY(@excl_q_terms::text[]) OR le.description ILIKE ANY(@excl_q_terms::text[])
       OR le.id = ANY(ARRAY(SELECT tt.transaction_id FROM transaction_tags tt JOIN tags g ON g.id = tt.tag_id WHERE g.user_id = @user_id AND g.name ILIKE ANY(@excl_q_terms::text[])))))
  AND (sqlc.narg(min_amount)::bigint IS NULL OR le.amount >= sqlc.narg(min_amount)::bigint)
  AND (sqlc.narg(max_amount)::bigint IS NULL OR le.amount <= sqlc.narg(max_amount)::bigint)
  AND le.type IN ('INCOME', 'EXPENSE', 'TRANSFER')
  AND le.source NOT IN ('ADJUSTMENT', 'LOAN')
  AND (le.type = 'TRANSFER' OR le.flow_amount > 0)
  AND (le.type = 'TRANSFER' OR le.kind <> 'TRANSACTION' OR le.account_id IS NULL OR a.include_in_balance OR cardinality(@account_ids::uuid[]) > 0)
  AND (leg.n = 0 OR le.type = 'TRANSFER')
GROUP BY 1;

-- name: ReportByCategory :many
-- Spending (or income) per category and currency; categories the caller cannot see count as uncategorized.
SELECT c.id AS category_id,
       le.currency::text AS currency,
       COALESCE(c.name, '')::text AS name,
       COALESCE(c.icon, '')::text AS icon,
       COALESCE(c.color, '')::text AS color,
       SUM(le.flow_amount)::bigint AS amount,
       COUNT(*)::int AS entries
FROM ledger_entries le
LEFT JOIN accounts a ON a.id = le.account_id
LEFT JOIN categories c ON c.id = le.category_id AND (c.is_system OR c.owner_id = le.user_id)
WHERE le.user_id = @user_id
  AND (CASE @planned::text WHEN 'only' THEN le.occurred_at IS NULL WHEN 'all' THEN true ELSE le.occurred_at IS NOT NULL END)
  AND (sqlc.narg(from_at)::timestamptz IS NULL OR COALESCE(le.occurred_at, le.due_at) >= sqlc.narg(from_at)::timestamptz)
  AND (sqlc.narg(to_at)::timestamptz IS NULL OR COALESCE(le.occurred_at, le.due_at) < sqlc.narg(to_at)::timestamptz)
  AND (cardinality(@kinds::text[]) = 0 OR le.kind = ANY(@kinds::text[]))
  AND (cardinality(@types::text[]) = 0 OR le.type = ANY(@types::text[]))
  AND (cardinality(@sources::text[]) = 0 OR le.source = ANY(@sources::text[]))
  AND (cardinality(@excl_sources::text[]) = 0 OR NOT (le.source = ANY(@excl_sources::text[])))
  AND (cardinality(@account_ids::uuid[]) = 0 OR le.account_id = ANY(@account_ids::uuid[]) OR le.to_account_id = ANY(@account_ids::uuid[]))
  AND ((cardinality(@category_ids::uuid[]) = 0 AND NOT @category_none::bool)
       OR le.category_id = ANY(@category_ids::uuid[])
       OR (@category_none::bool AND le.category_id IS NULL))
  AND (cardinality(@group_ids::uuid[]) = 0 OR le.group_id = ANY(@group_ids::uuid[]))
  AND (cardinality(@tag_ids::uuid[]) = 0 OR (le.kind = 'TRANSACTION' AND EXISTS (
        SELECT 1 FROM transaction_tags tt WHERE tt.transaction_id = le.id AND tt.tag_id = ANY(@tag_ids::uuid[]))))
  AND (cardinality(@excl_tag_ids::uuid[]) = 0 OR le.kind <> 'TRANSACTION' OR NOT EXISTS (
        SELECT 1 FROM transaction_tags tt WHERE tt.transaction_id = le.id AND tt.tag_id = ANY(@excl_tag_ids::uuid[])))
  AND (cardinality(@q_terms::text[]) < 1 OR le.title ILIKE (@q_terms::text[])[1] OR le.description ILIKE (@q_terms::text[])[1] OR le.id = ANY(ARRAY(SELECT tt.transaction_id FROM transaction_tags tt JOIN tags g ON g.id = tt.tag_id WHERE g.user_id = @user_id AND g.name ILIKE (@q_terms::text[])[1])))
  AND (cardinality(@q_terms::text[]) < 2 OR le.title ILIKE (@q_terms::text[])[2] OR le.description ILIKE (@q_terms::text[])[2] OR le.id = ANY(ARRAY(SELECT tt.transaction_id FROM transaction_tags tt JOIN tags g ON g.id = tt.tag_id WHERE g.user_id = @user_id AND g.name ILIKE (@q_terms::text[])[2])))
  AND (cardinality(@q_terms::text[]) < 3 OR le.title ILIKE (@q_terms::text[])[3] OR le.description ILIKE (@q_terms::text[])[3] OR le.id = ANY(ARRAY(SELECT tt.transaction_id FROM transaction_tags tt JOIN tags g ON g.id = tt.tag_id WHERE g.user_id = @user_id AND g.name ILIKE (@q_terms::text[])[3])))
  AND (cardinality(@q_terms::text[]) < 4 OR le.title ILIKE (@q_terms::text[])[4] OR le.description ILIKE (@q_terms::text[])[4] OR le.id = ANY(ARRAY(SELECT tt.transaction_id FROM transaction_tags tt JOIN tags g ON g.id = tt.tag_id WHERE g.user_id = @user_id AND g.name ILIKE (@q_terms::text[])[4])))
  AND (cardinality(@q_terms::text[]) < 5 OR le.title ILIKE (@q_terms::text[])[5] OR le.description ILIKE (@q_terms::text[])[5] OR le.id = ANY(ARRAY(SELECT tt.transaction_id FROM transaction_tags tt JOIN tags g ON g.id = tt.tag_id WHERE g.user_id = @user_id AND g.name ILIKE (@q_terms::text[])[5])))
  AND (cardinality(@q_terms::text[]) < 6 OR le.title ILIKE (@q_terms::text[])[6] OR le.description ILIKE (@q_terms::text[])[6] OR le.id = ANY(ARRAY(SELECT tt.transaction_id FROM transaction_tags tt JOIN tags g ON g.id = tt.tag_id WHERE g.user_id = @user_id AND g.name ILIKE (@q_terms::text[])[6])))
  AND (cardinality(@q_terms::text[]) < 7 OR le.title ILIKE (@q_terms::text[])[7] OR le.description ILIKE (@q_terms::text[])[7] OR le.id = ANY(ARRAY(SELECT tt.transaction_id FROM transaction_tags tt JOIN tags g ON g.id = tt.tag_id WHERE g.user_id = @user_id AND g.name ILIKE (@q_terms::text[])[7])))
  AND (cardinality(@q_terms::text[]) < 8 OR le.title ILIKE (@q_terms::text[])[8] OR le.description ILIKE (@q_terms::text[])[8] OR le.id = ANY(ARRAY(SELECT tt.transaction_id FROM transaction_tags tt JOIN tags g ON g.id = tt.tag_id WHERE g.user_id = @user_id AND g.name ILIKE (@q_terms::text[])[8])))
  AND (cardinality(@q_terms::text[]) < 9 OR le.title ILIKE (@q_terms::text[])[9] OR le.description ILIKE (@q_terms::text[])[9] OR le.id = ANY(ARRAY(SELECT tt.transaction_id FROM transaction_tags tt JOIN tags g ON g.id = tt.tag_id WHERE g.user_id = @user_id AND g.name ILIKE (@q_terms::text[])[9])))
  AND (cardinality(@q_terms::text[]) < 10 OR le.title ILIKE (@q_terms::text[])[10] OR le.description ILIKE (@q_terms::text[])[10] OR le.id = ANY(ARRAY(SELECT tt.transaction_id FROM transaction_tags tt JOIN tags g ON g.id = tt.tag_id WHERE g.user_id = @user_id AND g.name ILIKE (@q_terms::text[])[10])))
  AND (cardinality(@excl_q_terms::text[]) = 0 OR NOT (le.title ILIKE ANY(@excl_q_terms::text[]) OR le.description ILIKE ANY(@excl_q_terms::text[])
       OR le.id = ANY(ARRAY(SELECT tt.transaction_id FROM transaction_tags tt JOIN tags g ON g.id = tt.tag_id WHERE g.user_id = @user_id AND g.name ILIKE ANY(@excl_q_terms::text[])))))
  AND (sqlc.narg(min_amount)::bigint IS NULL OR le.amount >= sqlc.narg(min_amount)::bigint)
  AND (sqlc.narg(max_amount)::bigint IS NULL OR le.amount <= sqlc.narg(max_amount)::bigint)
  AND le.type IN ('INCOME', 'EXPENSE', 'TRANSFER')
  AND le.source NOT IN ('ADJUSTMENT', 'LOAN')
  AND (le.type = 'TRANSFER' OR le.flow_amount > 0)
  AND (le.type = 'TRANSFER' OR le.kind <> 'TRANSACTION' OR le.account_id IS NULL OR a.include_in_balance OR cardinality(@account_ids::uuid[]) > 0)
  AND le.type = @breakdown::text
GROUP BY c.id, c.name, c.icon, c.color, le.currency;

-- name: ReportByAccount :many
-- Per account (NULL = no account: group shares someone else paid) and entry currency.
SELECT acc.id AS account_id,
       COALESCE(acc.name, '')::text AS name,
       COALESCE(acc.currency, '')::text AS account_currency,
       (CASE WHEN leg.n = 0 THEN le.currency ELSE COALESCE(le.to_currency, le.currency) END)::text AS currency,
       COALESCE(SUM(le.flow_amount) FILTER (WHERE leg.n = 0 AND le.type = 'INCOME'), 0)::bigint AS income,
       COALESCE(SUM(le.flow_amount) FILTER (WHERE leg.n = 0 AND le.type = 'EXPENSE'), 0)::bigint AS expense,
       COALESCE(SUM(le.amount) FILTER (WHERE leg.n = 0 AND le.type = 'TRANSFER' AND (cardinality(@account_ids::uuid[]) = 0 OR le.account_id = ANY(@account_ids::uuid[]))), 0)::bigint AS transfers_out,
       COALESCE(SUM(COALESCE(le.to_amount, le.amount)) FILTER (WHERE leg.n = 1 AND (cardinality(@account_ids::uuid[]) = 0 OR le.to_account_id = ANY(@account_ids::uuid[]))), 0)::bigint AS transfers_in
FROM ledger_entries le
LEFT JOIN accounts a ON a.id = le.account_id
CROSS JOIN LATERAL (VALUES (0), (1)) AS leg(n)
LEFT JOIN accounts acc ON acc.id = (CASE leg.n WHEN 0 THEN le.account_id ELSE le.to_account_id END)
WHERE le.user_id = @user_id
  AND (CASE @planned::text WHEN 'only' THEN le.occurred_at IS NULL WHEN 'all' THEN true ELSE le.occurred_at IS NOT NULL END)
  AND (sqlc.narg(from_at)::timestamptz IS NULL OR COALESCE(le.occurred_at, le.due_at) >= sqlc.narg(from_at)::timestamptz)
  AND (sqlc.narg(to_at)::timestamptz IS NULL OR COALESCE(le.occurred_at, le.due_at) < sqlc.narg(to_at)::timestamptz)
  AND (cardinality(@kinds::text[]) = 0 OR le.kind = ANY(@kinds::text[]))
  AND (cardinality(@types::text[]) = 0 OR le.type = ANY(@types::text[]))
  AND (cardinality(@sources::text[]) = 0 OR le.source = ANY(@sources::text[]))
  AND (cardinality(@excl_sources::text[]) = 0 OR NOT (le.source = ANY(@excl_sources::text[])))
  AND (cardinality(@account_ids::uuid[]) = 0 OR le.account_id = ANY(@account_ids::uuid[]) OR le.to_account_id = ANY(@account_ids::uuid[]))
  AND ((cardinality(@category_ids::uuid[]) = 0 AND NOT @category_none::bool)
       OR le.category_id = ANY(@category_ids::uuid[])
       OR (@category_none::bool AND le.category_id IS NULL))
  AND (cardinality(@group_ids::uuid[]) = 0 OR le.group_id = ANY(@group_ids::uuid[]))
  AND (cardinality(@tag_ids::uuid[]) = 0 OR (le.kind = 'TRANSACTION' AND EXISTS (
        SELECT 1 FROM transaction_tags tt WHERE tt.transaction_id = le.id AND tt.tag_id = ANY(@tag_ids::uuid[]))))
  AND (cardinality(@excl_tag_ids::uuid[]) = 0 OR le.kind <> 'TRANSACTION' OR NOT EXISTS (
        SELECT 1 FROM transaction_tags tt WHERE tt.transaction_id = le.id AND tt.tag_id = ANY(@excl_tag_ids::uuid[])))
  AND (cardinality(@q_terms::text[]) < 1 OR le.title ILIKE (@q_terms::text[])[1] OR le.description ILIKE (@q_terms::text[])[1] OR le.id = ANY(ARRAY(SELECT tt.transaction_id FROM transaction_tags tt JOIN tags g ON g.id = tt.tag_id WHERE g.user_id = @user_id AND g.name ILIKE (@q_terms::text[])[1])))
  AND (cardinality(@q_terms::text[]) < 2 OR le.title ILIKE (@q_terms::text[])[2] OR le.description ILIKE (@q_terms::text[])[2] OR le.id = ANY(ARRAY(SELECT tt.transaction_id FROM transaction_tags tt JOIN tags g ON g.id = tt.tag_id WHERE g.user_id = @user_id AND g.name ILIKE (@q_terms::text[])[2])))
  AND (cardinality(@q_terms::text[]) < 3 OR le.title ILIKE (@q_terms::text[])[3] OR le.description ILIKE (@q_terms::text[])[3] OR le.id = ANY(ARRAY(SELECT tt.transaction_id FROM transaction_tags tt JOIN tags g ON g.id = tt.tag_id WHERE g.user_id = @user_id AND g.name ILIKE (@q_terms::text[])[3])))
  AND (cardinality(@q_terms::text[]) < 4 OR le.title ILIKE (@q_terms::text[])[4] OR le.description ILIKE (@q_terms::text[])[4] OR le.id = ANY(ARRAY(SELECT tt.transaction_id FROM transaction_tags tt JOIN tags g ON g.id = tt.tag_id WHERE g.user_id = @user_id AND g.name ILIKE (@q_terms::text[])[4])))
  AND (cardinality(@q_terms::text[]) < 5 OR le.title ILIKE (@q_terms::text[])[5] OR le.description ILIKE (@q_terms::text[])[5] OR le.id = ANY(ARRAY(SELECT tt.transaction_id FROM transaction_tags tt JOIN tags g ON g.id = tt.tag_id WHERE g.user_id = @user_id AND g.name ILIKE (@q_terms::text[])[5])))
  AND (cardinality(@q_terms::text[]) < 6 OR le.title ILIKE (@q_terms::text[])[6] OR le.description ILIKE (@q_terms::text[])[6] OR le.id = ANY(ARRAY(SELECT tt.transaction_id FROM transaction_tags tt JOIN tags g ON g.id = tt.tag_id WHERE g.user_id = @user_id AND g.name ILIKE (@q_terms::text[])[6])))
  AND (cardinality(@q_terms::text[]) < 7 OR le.title ILIKE (@q_terms::text[])[7] OR le.description ILIKE (@q_terms::text[])[7] OR le.id = ANY(ARRAY(SELECT tt.transaction_id FROM transaction_tags tt JOIN tags g ON g.id = tt.tag_id WHERE g.user_id = @user_id AND g.name ILIKE (@q_terms::text[])[7])))
  AND (cardinality(@q_terms::text[]) < 8 OR le.title ILIKE (@q_terms::text[])[8] OR le.description ILIKE (@q_terms::text[])[8] OR le.id = ANY(ARRAY(SELECT tt.transaction_id FROM transaction_tags tt JOIN tags g ON g.id = tt.tag_id WHERE g.user_id = @user_id AND g.name ILIKE (@q_terms::text[])[8])))
  AND (cardinality(@q_terms::text[]) < 9 OR le.title ILIKE (@q_terms::text[])[9] OR le.description ILIKE (@q_terms::text[])[9] OR le.id = ANY(ARRAY(SELECT tt.transaction_id FROM transaction_tags tt JOIN tags g ON g.id = tt.tag_id WHERE g.user_id = @user_id AND g.name ILIKE (@q_terms::text[])[9])))
  AND (cardinality(@q_terms::text[]) < 10 OR le.title ILIKE (@q_terms::text[])[10] OR le.description ILIKE (@q_terms::text[])[10] OR le.id = ANY(ARRAY(SELECT tt.transaction_id FROM transaction_tags tt JOIN tags g ON g.id = tt.tag_id WHERE g.user_id = @user_id AND g.name ILIKE (@q_terms::text[])[10])))
  AND (cardinality(@excl_q_terms::text[]) = 0 OR NOT (le.title ILIKE ANY(@excl_q_terms::text[]) OR le.description ILIKE ANY(@excl_q_terms::text[])
       OR le.id = ANY(ARRAY(SELECT tt.transaction_id FROM transaction_tags tt JOIN tags g ON g.id = tt.tag_id WHERE g.user_id = @user_id AND g.name ILIKE ANY(@excl_q_terms::text[])))))
  AND (sqlc.narg(min_amount)::bigint IS NULL OR le.amount >= sqlc.narg(min_amount)::bigint)
  AND (sqlc.narg(max_amount)::bigint IS NULL OR le.amount <= sqlc.narg(max_amount)::bigint)
  AND le.type IN ('INCOME', 'EXPENSE', 'TRANSFER')
  AND le.source NOT IN ('ADJUSTMENT', 'LOAN')
  AND (le.type = 'TRANSFER' OR le.flow_amount > 0)
  AND (le.type = 'TRANSFER' OR le.kind <> 'TRANSACTION' OR le.account_id IS NULL OR a.include_in_balance OR cardinality(@account_ids::uuid[]) > 0)
  AND (leg.n = 0 OR le.type = 'TRANSFER')
  AND (leg.n = 1 OR le.type <> 'TRANSFER' OR (cardinality(@account_ids::uuid[]) = 0 OR le.account_id = ANY(@account_ids::uuid[])))
  AND (leg.n = 0 OR (cardinality(@account_ids::uuid[]) = 0 OR le.to_account_id = ANY(@account_ids::uuid[])))
GROUP BY acc.id, acc.name, acc.currency, 4;

-- name: ReportTrend :many
-- Same aggregates as ReportTotals per UTC bucket (day, ISO week starting Monday, or month starting on
-- start_day) and currency.
SELECT (CASE sqlc.arg(bucket)::text
          WHEN 'day' THEN date_trunc('day', COALESCE(le.occurred_at, le.due_at) AT TIME ZONE 'UTC')
          WHEN 'week' THEN date_trunc('week', COALESCE(le.occurred_at, le.due_at) AT TIME ZONE 'UTC')
          ELSE date_trunc('month', (COALESCE(le.occurred_at, le.due_at) AT TIME ZONE 'UTC') - ((sqlc.arg(start_day)::int - 1) * interval '1 day'))
               + ((sqlc.arg(start_day)::int - 1) * interval '1 day')
        END)::timestamp AS bucket_start,
       (CASE WHEN leg.n = 0 THEN le.currency ELSE COALESCE(le.to_currency, le.currency) END)::text AS currency,
       COALESCE(SUM(le.flow_amount) FILTER (WHERE leg.n = 0 AND le.type = 'INCOME'), 0)::bigint AS income,
       COALESCE(SUM(le.flow_amount) FILTER (WHERE leg.n = 0 AND le.type = 'EXPENSE'), 0)::bigint AS expense,
       COALESCE(SUM(le.amount) FILTER (WHERE leg.n = 0 AND le.type = 'TRANSFER' AND (cardinality(@account_ids::uuid[]) = 0 OR le.account_id = ANY(@account_ids::uuid[]))), 0)::bigint AS transfers_out,
       COALESCE(SUM(COALESCE(le.to_amount, le.amount)) FILTER (WHERE leg.n = 1 AND (cardinality(@account_ids::uuid[]) = 0 OR le.to_account_id = ANY(@account_ids::uuid[]))), 0)::bigint AS transfers_in
FROM ledger_entries le
LEFT JOIN accounts a ON a.id = le.account_id
CROSS JOIN LATERAL (VALUES (0), (1)) AS leg(n)
WHERE le.user_id = @user_id
  AND (CASE @planned::text WHEN 'only' THEN le.occurred_at IS NULL WHEN 'all' THEN true ELSE le.occurred_at IS NOT NULL END)
  AND (sqlc.narg(from_at)::timestamptz IS NULL OR COALESCE(le.occurred_at, le.due_at) >= sqlc.narg(from_at)::timestamptz)
  AND (sqlc.narg(to_at)::timestamptz IS NULL OR COALESCE(le.occurred_at, le.due_at) < sqlc.narg(to_at)::timestamptz)
  AND (cardinality(@kinds::text[]) = 0 OR le.kind = ANY(@kinds::text[]))
  AND (cardinality(@types::text[]) = 0 OR le.type = ANY(@types::text[]))
  AND (cardinality(@sources::text[]) = 0 OR le.source = ANY(@sources::text[]))
  AND (cardinality(@excl_sources::text[]) = 0 OR NOT (le.source = ANY(@excl_sources::text[])))
  AND (cardinality(@account_ids::uuid[]) = 0 OR le.account_id = ANY(@account_ids::uuid[]) OR le.to_account_id = ANY(@account_ids::uuid[]))
  AND ((cardinality(@category_ids::uuid[]) = 0 AND NOT @category_none::bool)
       OR le.category_id = ANY(@category_ids::uuid[])
       OR (@category_none::bool AND le.category_id IS NULL))
  AND (cardinality(@group_ids::uuid[]) = 0 OR le.group_id = ANY(@group_ids::uuid[]))
  AND (cardinality(@tag_ids::uuid[]) = 0 OR (le.kind = 'TRANSACTION' AND EXISTS (
        SELECT 1 FROM transaction_tags tt WHERE tt.transaction_id = le.id AND tt.tag_id = ANY(@tag_ids::uuid[]))))
  AND (cardinality(@excl_tag_ids::uuid[]) = 0 OR le.kind <> 'TRANSACTION' OR NOT EXISTS (
        SELECT 1 FROM transaction_tags tt WHERE tt.transaction_id = le.id AND tt.tag_id = ANY(@excl_tag_ids::uuid[])))
  AND (cardinality(@q_terms::text[]) < 1 OR le.title ILIKE (@q_terms::text[])[1] OR le.description ILIKE (@q_terms::text[])[1] OR le.id = ANY(ARRAY(SELECT tt.transaction_id FROM transaction_tags tt JOIN tags g ON g.id = tt.tag_id WHERE g.user_id = @user_id AND g.name ILIKE (@q_terms::text[])[1])))
  AND (cardinality(@q_terms::text[]) < 2 OR le.title ILIKE (@q_terms::text[])[2] OR le.description ILIKE (@q_terms::text[])[2] OR le.id = ANY(ARRAY(SELECT tt.transaction_id FROM transaction_tags tt JOIN tags g ON g.id = tt.tag_id WHERE g.user_id = @user_id AND g.name ILIKE (@q_terms::text[])[2])))
  AND (cardinality(@q_terms::text[]) < 3 OR le.title ILIKE (@q_terms::text[])[3] OR le.description ILIKE (@q_terms::text[])[3] OR le.id = ANY(ARRAY(SELECT tt.transaction_id FROM transaction_tags tt JOIN tags g ON g.id = tt.tag_id WHERE g.user_id = @user_id AND g.name ILIKE (@q_terms::text[])[3])))
  AND (cardinality(@q_terms::text[]) < 4 OR le.title ILIKE (@q_terms::text[])[4] OR le.description ILIKE (@q_terms::text[])[4] OR le.id = ANY(ARRAY(SELECT tt.transaction_id FROM transaction_tags tt JOIN tags g ON g.id = tt.tag_id WHERE g.user_id = @user_id AND g.name ILIKE (@q_terms::text[])[4])))
  AND (cardinality(@q_terms::text[]) < 5 OR le.title ILIKE (@q_terms::text[])[5] OR le.description ILIKE (@q_terms::text[])[5] OR le.id = ANY(ARRAY(SELECT tt.transaction_id FROM transaction_tags tt JOIN tags g ON g.id = tt.tag_id WHERE g.user_id = @user_id AND g.name ILIKE (@q_terms::text[])[5])))
  AND (cardinality(@q_terms::text[]) < 6 OR le.title ILIKE (@q_terms::text[])[6] OR le.description ILIKE (@q_terms::text[])[6] OR le.id = ANY(ARRAY(SELECT tt.transaction_id FROM transaction_tags tt JOIN tags g ON g.id = tt.tag_id WHERE g.user_id = @user_id AND g.name ILIKE (@q_terms::text[])[6])))
  AND (cardinality(@q_terms::text[]) < 7 OR le.title ILIKE (@q_terms::text[])[7] OR le.description ILIKE (@q_terms::text[])[7] OR le.id = ANY(ARRAY(SELECT tt.transaction_id FROM transaction_tags tt JOIN tags g ON g.id = tt.tag_id WHERE g.user_id = @user_id AND g.name ILIKE (@q_terms::text[])[7])))
  AND (cardinality(@q_terms::text[]) < 8 OR le.title ILIKE (@q_terms::text[])[8] OR le.description ILIKE (@q_terms::text[])[8] OR le.id = ANY(ARRAY(SELECT tt.transaction_id FROM transaction_tags tt JOIN tags g ON g.id = tt.tag_id WHERE g.user_id = @user_id AND g.name ILIKE (@q_terms::text[])[8])))
  AND (cardinality(@q_terms::text[]) < 9 OR le.title ILIKE (@q_terms::text[])[9] OR le.description ILIKE (@q_terms::text[])[9] OR le.id = ANY(ARRAY(SELECT tt.transaction_id FROM transaction_tags tt JOIN tags g ON g.id = tt.tag_id WHERE g.user_id = @user_id AND g.name ILIKE (@q_terms::text[])[9])))
  AND (cardinality(@q_terms::text[]) < 10 OR le.title ILIKE (@q_terms::text[])[10] OR le.description ILIKE (@q_terms::text[])[10] OR le.id = ANY(ARRAY(SELECT tt.transaction_id FROM transaction_tags tt JOIN tags g ON g.id = tt.tag_id WHERE g.user_id = @user_id AND g.name ILIKE (@q_terms::text[])[10])))
  AND (cardinality(@excl_q_terms::text[]) = 0 OR NOT (le.title ILIKE ANY(@excl_q_terms::text[]) OR le.description ILIKE ANY(@excl_q_terms::text[])
       OR le.id = ANY(ARRAY(SELECT tt.transaction_id FROM transaction_tags tt JOIN tags g ON g.id = tt.tag_id WHERE g.user_id = @user_id AND g.name ILIKE ANY(@excl_q_terms::text[])))))
  AND (sqlc.narg(min_amount)::bigint IS NULL OR le.amount >= sqlc.narg(min_amount)::bigint)
  AND (sqlc.narg(max_amount)::bigint IS NULL OR le.amount <= sqlc.narg(max_amount)::bigint)
  AND le.type IN ('INCOME', 'EXPENSE', 'TRANSFER')
  AND le.source NOT IN ('ADJUSTMENT', 'LOAN')
  AND (le.type = 'TRANSFER' OR le.flow_amount > 0)
  AND (le.type = 'TRANSFER' OR le.kind <> 'TRANSACTION' OR le.account_id IS NULL OR a.include_in_balance OR cardinality(@account_ids::uuid[]) > 0)
  AND (leg.n = 0 OR le.type = 'TRANSFER')
GROUP BY 1, 2;

-- name: ReportEntriesCount :one
-- Exactly the rows ListLedgerEntries returns for the same filters (no report-only exclusions).
SELECT COUNT(*)::bigint AS entries FROM ledger_entries le
WHERE le.user_id = @user_id
  AND (CASE @planned::text WHEN 'only' THEN le.occurred_at IS NULL WHEN 'all' THEN true ELSE le.occurred_at IS NOT NULL END)
  AND (sqlc.narg(from_at)::timestamptz IS NULL OR COALESCE(le.occurred_at, le.due_at) >= sqlc.narg(from_at)::timestamptz)
  AND (sqlc.narg(to_at)::timestamptz IS NULL OR COALESCE(le.occurred_at, le.due_at) < sqlc.narg(to_at)::timestamptz)
  AND (cardinality(@kinds::text[]) = 0 OR le.kind = ANY(@kinds::text[]))
  AND (cardinality(@types::text[]) = 0 OR le.type = ANY(@types::text[]))
  AND (cardinality(@sources::text[]) = 0 OR le.source = ANY(@sources::text[]))
  AND (cardinality(@excl_sources::text[]) = 0 OR NOT (le.source = ANY(@excl_sources::text[])))
  AND (cardinality(@account_ids::uuid[]) = 0 OR le.account_id = ANY(@account_ids::uuid[]) OR le.to_account_id = ANY(@account_ids::uuid[]))
  AND ((cardinality(@category_ids::uuid[]) = 0 AND NOT @category_none::bool)
       OR le.category_id = ANY(@category_ids::uuid[])
       OR (@category_none::bool AND le.category_id IS NULL))
  AND (cardinality(@group_ids::uuid[]) = 0 OR le.group_id = ANY(@group_ids::uuid[]))
  AND (cardinality(@tag_ids::uuid[]) = 0 OR (le.kind = 'TRANSACTION' AND EXISTS (
        SELECT 1 FROM transaction_tags tt WHERE tt.transaction_id = le.id AND tt.tag_id = ANY(@tag_ids::uuid[]))))
  AND (cardinality(@excl_tag_ids::uuid[]) = 0 OR le.kind <> 'TRANSACTION' OR NOT EXISTS (
        SELECT 1 FROM transaction_tags tt WHERE tt.transaction_id = le.id AND tt.tag_id = ANY(@excl_tag_ids::uuid[])))
  AND (cardinality(@q_terms::text[]) < 1 OR le.title ILIKE (@q_terms::text[])[1] OR le.description ILIKE (@q_terms::text[])[1] OR le.id = ANY(ARRAY(SELECT tt.transaction_id FROM transaction_tags tt JOIN tags g ON g.id = tt.tag_id WHERE g.user_id = @user_id AND g.name ILIKE (@q_terms::text[])[1])))
  AND (cardinality(@q_terms::text[]) < 2 OR le.title ILIKE (@q_terms::text[])[2] OR le.description ILIKE (@q_terms::text[])[2] OR le.id = ANY(ARRAY(SELECT tt.transaction_id FROM transaction_tags tt JOIN tags g ON g.id = tt.tag_id WHERE g.user_id = @user_id AND g.name ILIKE (@q_terms::text[])[2])))
  AND (cardinality(@q_terms::text[]) < 3 OR le.title ILIKE (@q_terms::text[])[3] OR le.description ILIKE (@q_terms::text[])[3] OR le.id = ANY(ARRAY(SELECT tt.transaction_id FROM transaction_tags tt JOIN tags g ON g.id = tt.tag_id WHERE g.user_id = @user_id AND g.name ILIKE (@q_terms::text[])[3])))
  AND (cardinality(@q_terms::text[]) < 4 OR le.title ILIKE (@q_terms::text[])[4] OR le.description ILIKE (@q_terms::text[])[4] OR le.id = ANY(ARRAY(SELECT tt.transaction_id FROM transaction_tags tt JOIN tags g ON g.id = tt.tag_id WHERE g.user_id = @user_id AND g.name ILIKE (@q_terms::text[])[4])))
  AND (cardinality(@q_terms::text[]) < 5 OR le.title ILIKE (@q_terms::text[])[5] OR le.description ILIKE (@q_terms::text[])[5] OR le.id = ANY(ARRAY(SELECT tt.transaction_id FROM transaction_tags tt JOIN tags g ON g.id = tt.tag_id WHERE g.user_id = @user_id AND g.name ILIKE (@q_terms::text[])[5])))
  AND (cardinality(@q_terms::text[]) < 6 OR le.title ILIKE (@q_terms::text[])[6] OR le.description ILIKE (@q_terms::text[])[6] OR le.id = ANY(ARRAY(SELECT tt.transaction_id FROM transaction_tags tt JOIN tags g ON g.id = tt.tag_id WHERE g.user_id = @user_id AND g.name ILIKE (@q_terms::text[])[6])))
  AND (cardinality(@q_terms::text[]) < 7 OR le.title ILIKE (@q_terms::text[])[7] OR le.description ILIKE (@q_terms::text[])[7] OR le.id = ANY(ARRAY(SELECT tt.transaction_id FROM transaction_tags tt JOIN tags g ON g.id = tt.tag_id WHERE g.user_id = @user_id AND g.name ILIKE (@q_terms::text[])[7])))
  AND (cardinality(@q_terms::text[]) < 8 OR le.title ILIKE (@q_terms::text[])[8] OR le.description ILIKE (@q_terms::text[])[8] OR le.id = ANY(ARRAY(SELECT tt.transaction_id FROM transaction_tags tt JOIN tags g ON g.id = tt.tag_id WHERE g.user_id = @user_id AND g.name ILIKE (@q_terms::text[])[8])))
  AND (cardinality(@q_terms::text[]) < 9 OR le.title ILIKE (@q_terms::text[])[9] OR le.description ILIKE (@q_terms::text[])[9] OR le.id = ANY(ARRAY(SELECT tt.transaction_id FROM transaction_tags tt JOIN tags g ON g.id = tt.tag_id WHERE g.user_id = @user_id AND g.name ILIKE (@q_terms::text[])[9])))
  AND (cardinality(@q_terms::text[]) < 10 OR le.title ILIKE (@q_terms::text[])[10] OR le.description ILIKE (@q_terms::text[])[10] OR le.id = ANY(ARRAY(SELECT tt.transaction_id FROM transaction_tags tt JOIN tags g ON g.id = tt.tag_id WHERE g.user_id = @user_id AND g.name ILIKE (@q_terms::text[])[10])))
  AND (cardinality(@excl_q_terms::text[]) = 0 OR NOT (le.title ILIKE ANY(@excl_q_terms::text[]) OR le.description ILIKE ANY(@excl_q_terms::text[])
       OR le.id = ANY(ARRAY(SELECT tt.transaction_id FROM transaction_tags tt JOIN tags g ON g.id = tt.tag_id WHERE g.user_id = @user_id AND g.name ILIKE ANY(@excl_q_terms::text[])))))
  AND (sqlc.narg(min_amount)::bigint IS NULL OR le.amount >= sqlc.narg(min_amount)::bigint)
  AND (sqlc.narg(max_amount)::bigint IS NULL OR le.amount <= sqlc.narg(max_amount)::bigint);


-- name: ReportAccountNames :many
SELECT id, name FROM accounts WHERE user_id = @user_id AND id = ANY(@ids::uuid[]);

-- name: ReportCategoryNames :many
SELECT id, name FROM categories WHERE id = ANY(@ids::uuid[]) AND (is_system OR owner_id = @user_id);
