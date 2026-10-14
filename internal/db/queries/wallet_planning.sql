-- Wallet (phase 2) queries: planned payments, budgets, loans. Handlers live in
-- internal/wallet (planned.go, budgets.go, loans.go).

-- ---------------------------------------------------------------- planned payments

-- name: CreatePlannedPayment :one
INSERT INTO planned_payments (
    user_id, type, account_id, to_account_id, amount, currency, to_amount, category_id, title, description,
    start_at, one_time, interval_n, interval_unit, active
) VALUES (
    @user_id, @type, sqlc.narg(account_id), sqlc.narg(to_account_id), @amount, @currency, sqlc.narg(to_amount),
    NULLIF(@category_id::uuid, '00000000-0000-0000-0000-000000000000'::uuid), @title, @description,
    @start_at, @one_time, sqlc.narg(interval_n), sqlc.narg(interval_unit), @active
)
RETURNING *;

-- name: GetPlannedPayment :one
SELECT * FROM planned_payments WHERE id = $1 AND user_id = $2 AND deleted_at IS NULL;

-- name: LockPlannedPayment :one
SELECT * FROM planned_payments WHERE id = $1 AND user_id = $2 AND deleted_at IS NULL FOR UPDATE;

-- name: ListPlannedPayments :many
SELECT * FROM planned_payments
WHERE user_id = @user_id AND deleted_at IS NULL
  AND (cardinality(@types::text[]) = 0 OR type = ANY(@types::text[]))
  AND (sqlc.narg(active)::bool IS NULL OR active = sqlc.narg(active)::bool)
ORDER BY created_at, id;

-- name: UpdatePlannedPayment :one
UPDATE planned_payments SET
    type = @type, account_id = sqlc.narg(account_id), to_account_id = sqlc.narg(to_account_id),
    amount = @amount, currency = @currency, to_amount = sqlc.narg(to_amount),
    category_id = NULLIF(@category_id::uuid, '00000000-0000-0000-0000-000000000000'::uuid),
    title = @title, description = @description, start_at = @start_at, one_time = @one_time,
    interval_n = sqlc.narg(interval_n), interval_unit = sqlc.narg(interval_unit), active = @active,
    resolved_until = sqlc.narg(resolved_until), updated_at = now()
WHERE id = @id AND user_id = @user_id AND deleted_at IS NULL
RETURNING *;

-- name: SoftDeletePlannedPayment :execrows
UPDATE planned_payments SET deleted_at = now(), active = false, updated_at = now()
WHERE id = $1 AND user_id = $2 AND deleted_at IS NULL;

-- name: SoftDeleteAccountPlannedPayments :exec
UPDATE planned_payments SET deleted_at = now(), active = false, updated_at = now()
WHERE user_id = @user_id AND deleted_at IS NULL
  AND (account_id = @account_id::uuid OR to_account_id = @account_id::uuid);

-- name: ListPlannedPaymentTags :many
SELECT pt.planned_payment_id, t.id, t.name, t.color, t.icon
FROM planned_payment_tags pt
JOIN tags t ON t.id = pt.tag_id
WHERE pt.planned_payment_id = ANY(@ids::uuid[])
ORDER BY t.sort_order, lower(t.name);

-- name: DeletePlannedPaymentTags :exec
DELETE FROM planned_payment_tags WHERE planned_payment_id = $1;

-- name: AddPlannedPaymentTags :exec
INSERT INTO planned_payment_tags (planned_payment_id, tag_id)
SELECT @planned_payment_id::uuid, unnest(@tag_ids::uuid[])
ON CONFLICT DO NOTHING;

-- name: GetPendingForPlan :one
SELECT * FROM transactions
WHERE planned_payment_id = @planned_payment_id::uuid AND occurred_at IS NULL AND deleted_at IS NULL;

-- name: ListPendingForPlans :many
SELECT * FROM transactions
WHERE planned_payment_id = ANY(@ids::uuid[]) AND occurred_at IS NULL AND deleted_at IS NULL;

-- name: LockTransaction :one
SELECT * FROM transactions WHERE id = $1 AND user_id = $2 AND deleted_at IS NULL FOR UPDATE;

-- name: ListPlannedTransactions :many
-- Unpaid planned items (due_at set, occurred_at NULL) due before due_before
-- (all when NULL), optionally only those touching account_id or in category_id.
SELECT t.* FROM transactions t
WHERE t.user_id = @user_id AND t.deleted_at IS NULL AND t.occurred_at IS NULL AND t.due_at IS NOT NULL
  AND (sqlc.narg(due_before)::timestamptz IS NULL OR t.due_at < sqlc.narg(due_before)::timestamptz)
  AND (sqlc.narg(account_id)::uuid IS NULL OR t.account_id = sqlc.narg(account_id)::uuid
       OR t.to_account_id = sqlc.narg(account_id)::uuid)
  AND (sqlc.narg(category_id)::uuid IS NULL OR t.category_id = sqlc.narg(category_id)::uuid)
ORDER BY t.due_at, t.id;

-- name: ListPlannedPaymentsByIDs :many
SELECT * FROM planned_payments WHERE id = ANY(@ids::uuid[]);

-- ---------------------------------------------------------------- budgets

-- name: ListBudgets :many
SELECT * FROM budgets WHERE user_id = $1 ORDER BY sort_order, created_at, id;

-- name: GetBudget :one
SELECT * FROM budgets WHERE id = $1 AND user_id = $2;

-- name: NextBudgetSortOrder :one
SELECT COALESCE(MAX(sort_order) + 1, 0)::int FROM budgets WHERE user_id = $1;

-- name: CreateBudget :one
INSERT INTO budgets (user_id, name, amount, currency, category_ids, account_ids, sort_order)
VALUES (@user_id, @name, @amount, @currency, @category_ids::uuid[], @account_ids::uuid[], @sort_order)
RETURNING *;

-- name: UpdateBudget :one
UPDATE budgets SET name = @name, amount = @amount, currency = @currency,
    category_ids = @category_ids::uuid[], account_ids = @account_ids::uuid[], updated_at = now()
WHERE id = @id AND user_id = @user_id
RETURNING *;

-- name: DeleteBudget :execrows
DELETE FROM budgets WHERE id = $1 AND user_id = $2;

-- name: SetBudgetSortOrder :execrows
UPDATE budgets SET sort_order = $3, updated_at = now() WHERE id = $1 AND user_id = $2;

-- name: BudgetSpent :many
-- Expense flows per (budget, currency) within [from_at, to_at): wallet EXPENSE
-- transactions and my group-expense shares whose category is in the budget's
-- categories (all when empty). With account_ids set, only entries booked to one
-- of those accounts count (a group share only when I paid it from such an
-- account); with none, entries follow the overview rule (transactions on
-- accounts excluded from the balance are left out, group shares always count).
-- Balance adjustments and loan bookings are never spending.
SELECT b.id AS budget_id, le.currency::text AS currency, SUM(le.flow_amount)::bigint AS spent
FROM budgets b
JOIN ledger_entries le ON le.user_id = b.user_id
LEFT JOIN accounts a ON a.id = le.account_id
WHERE b.user_id = @user_id
  AND le.occurred_at IS NOT NULL
  AND le.type = 'EXPENSE'
  AND le.source NOT IN ('ADJUSTMENT', 'LOAN')
  AND le.flow_amount > 0
  AND le.occurred_at >= @from_at::timestamptz
  AND le.occurred_at < @to_at::timestamptz
  AND (cardinality(b.category_ids) = 0 OR le.category_id = ANY(b.category_ids))
  AND (CASE WHEN cardinality(b.account_ids) = 0 THEN (le.kind <> 'TRANSACTION' OR a.include_in_balance)
            ELSE le.account_id = ANY(b.account_ids) END)
GROUP BY b.id, le.currency;

-- name: CountOwnedAccounts :one
SELECT COUNT(*)::int FROM accounts WHERE user_id = @user_id AND deleted_at IS NULL AND id = ANY(@ids::uuid[]);

-- ---------------------------------------------------------------- loans

-- name: CreateLoan :one
INSERT INTO loans (user_id, name, type, amount, currency, color, icon, note, account_id, occurred_at, sort_order)
VALUES (@user_id, @name, @type, @amount, @currency, @color, @icon, @note, sqlc.narg(account_id), @occurred_at, @sort_order)
RETURNING *;

-- name: GetLoan :one
SELECT * FROM loans WHERE id = $1 AND user_id = $2 AND deleted_at IS NULL;

-- name: LockLoan :one
SELECT * FROM loans WHERE id = $1 AND user_id = $2 AND deleted_at IS NULL FOR UPDATE;

-- name: ListLoans :many
SELECT * FROM loans
WHERE user_id = @user_id AND deleted_at IS NULL
  AND (cardinality(@types::text[]) = 0 OR type = ANY(@types::text[]))
ORDER BY sort_order, created_at, id;

-- name: NextLoanSortOrder :one
SELECT COALESCE(MAX(sort_order) + 1, 0)::int FROM loans WHERE user_id = $1 AND deleted_at IS NULL;

-- name: UpdateLoan :one
UPDATE loans SET name = @name, type = @type, amount = @amount, currency = @currency, color = @color,
    icon = @icon, note = @note, account_id = sqlc.narg(account_id), occurred_at = @occurred_at, updated_at = now()
WHERE id = @id AND user_id = @user_id AND deleted_at IS NULL
RETURNING *;

-- name: SoftDeleteLoan :execrows
UPDATE loans SET deleted_at = now(), updated_at = now() WHERE id = $1 AND user_id = $2 AND deleted_at IS NULL;

-- name: SetLoanSortOrder :execrows
UPDATE loans SET sort_order = $3, updated_at = now() WHERE id = $1 AND user_id = $2 AND deleted_at IS NULL;

-- name: ClearAccountFromLoans :exec
UPDATE loans SET account_id = NULL, updated_at = now() WHERE account_id = $1;

-- name: ClearAccountFromLoanRecords :exec
UPDATE loan_records SET account_id = NULL, transaction_id = NULL, updated_at = now() WHERE account_id = $1;

-- name: LoanRecordTotals :many
SELECT loan_id,
       COALESCE(SUM(amount) FILTER (WHERE type = 'INCREASE'), 0)::bigint AS increased,
       COALESCE(SUM(amount) FILTER (WHERE type = 'DECREASE'), 0)::bigint AS decreased,
       COUNT(*)::int AS records
FROM loan_records
WHERE loan_id = ANY(@loan_ids::uuid[]) AND deleted_at IS NULL
GROUP BY loan_id;

-- name: CreateLoanRecord :one
INSERT INTO loan_records (loan_id, user_id, type, amount, note, occurred_at, account_id)
VALUES (@loan_id, @user_id, @type, @amount, @note, @occurred_at, sqlc.narg(account_id))
RETURNING *;

-- name: GetLoanRecord :one
SELECT r.* FROM loan_records r
JOIN loans l ON l.id = r.loan_id AND l.deleted_at IS NULL
WHERE r.id = $1 AND r.user_id = $2 AND r.deleted_at IS NULL;

-- name: ListLoanRecords :many
SELECT * FROM loan_records WHERE loan_id = $1 AND deleted_at IS NULL ORDER BY occurred_at DESC, created_at DESC, id DESC;

-- name: UpdateLoanRecord :one
UPDATE loan_records SET type = @type, amount = @amount, note = @note, occurred_at = @occurred_at,
    account_id = sqlc.narg(account_id), updated_at = now()
WHERE id = @id AND user_id = @user_id AND deleted_at IS NULL
RETURNING *;

-- name: SetLoanRecordTransaction :exec
UPDATE loan_records SET transaction_id = sqlc.narg(transaction_id), updated_at = now() WHERE id = @id;

-- name: SoftDeleteLoanRecord :execrows
UPDATE loan_records SET deleted_at = now(), updated_at = now() WHERE id = $1 AND user_id = $2 AND deleted_at IS NULL;

-- name: SoftDeleteLoanRecordsByLoan :exec
UPDATE loan_records SET deleted_at = now(), updated_at = now() WHERE loan_id = $1 AND deleted_at IS NULL;

-- name: GetLoanTransaction :one
-- The transaction booking the loan itself (not one of its records).
SELECT * FROM transactions
WHERE loan_id = @loan_id::uuid AND loan_record_id IS NULL AND source = 'LOAN' AND deleted_at IS NULL
LIMIT 1;

-- name: SoftDeleteLoanTransactions :exec
UPDATE transactions SET deleted_at = now(), updated_at = now()
WHERE loan_id = @loan_id::uuid AND source = 'LOAN' AND deleted_at IS NULL;

-- name: SoftDeleteTransactionByID :exec
UPDATE transactions SET deleted_at = now(), updated_at = now() WHERE id = $1 AND deleted_at IS NULL;

-- name: LoanBookingUpdate :one
-- Rewrites a loan-managed transaction (only loan endpoints call this).
UPDATE transactions SET type = @type, account_id = sqlc.narg(account_id), amount = @amount, currency = @currency,
    title = @title, description = @description, occurred_at = @occurred_at, updated_at = now()
WHERE id = @id AND source = 'LOAN' AND deleted_at IS NULL
RETURNING *;
