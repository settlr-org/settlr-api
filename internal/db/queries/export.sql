-- Export domain queries for sqlc with pgx/v5.
-- These replace manual Pool.Query/Exec calls in internal/export/handlers.go
-- Handler usage: Handler{Pool *pgxpool.Pool, Queries *db.Queries} where Queries wraps the pool.

-- name: ExportMyCSV :many
SELECT e.expense_date, g.name AS group_name, e.description, coalesce(c.name, '') AS category,
       pu.name AS paid_by, e.amount, e.currency, e.split_mode,
       su.name AS split_user, coalesce(s.amount, 0)::bigint AS split_amount
FROM expenses e
JOIN groups g ON g.id = e.group_id
JOIN group_members gm ON gm.group_id = e.group_id AND gm.user_id = $1
JOIN users pu ON pu.id = e.paid_by
LEFT JOIN categories c ON c.id = e.category_id
LEFT JOIN expense_splits s ON s.expense_id = e.id
LEFT JOIN users su ON su.id = s.user_id
WHERE e.deleted_at IS NULL
ORDER BY e.expense_date, e.created_at;

-- name: ExportMyJSON :many
SELECT e.id, e.group_id, g.name AS group_name, e.description, coalesce(c.name, '') AS category, pu.name AS paid_by, e.amount, e.currency, e.split_mode, e.expense_date, e.created_at
FROM expenses e
JOIN groups g ON g.id = e.group_id
JOIN group_members gm ON gm.group_id = e.group_id AND gm.user_id = $1
JOIN users pu ON pu.id = e.paid_by
LEFT JOIN categories c ON c.id = e.category_id
WHERE e.deleted_at IS NULL
ORDER BY e.expense_date, e.created_at;

-- name: ExportGroupJSON :many
SELECT e.id, e.description, coalesce(c.name, '') AS category, pu.name AS paid_by, e.amount, e.currency, e.split_mode, e.expense_date, e.created_at, e.notes
FROM expenses e
JOIN users pu ON pu.id = e.paid_by
LEFT JOIN categories c ON c.id = e.category_id
WHERE e.group_id = $1 AND e.deleted_at IS NULL
ORDER BY e.expense_date, e.created_at;

-- name: ExportGroupCSV :many
SELECT e.expense_date, e.description, coalesce(c.name, '') AS category,
       pu.name AS paid_by, e.amount, e.currency, e.split_mode,
       su.name AS split_user, coalesce(s.amount, 0)::bigint AS split_amount
FROM expenses e
JOIN users pu ON pu.id = e.paid_by
LEFT JOIN categories c ON c.id = e.category_id
LEFT JOIN expense_splits s ON s.expense_id = e.id
LEFT JOIN users su ON su.id = s.user_id
WHERE e.group_id = $1 AND e.deleted_at IS NULL
ORDER BY e.expense_date, e.created_at;

-- name: ExportGroupSettlements :many
SELECT st.settled_at, f.name AS from_name, t.name AS to_name, st.amount, st.currency, st.note
FROM settlements st
JOIN users f ON f.id = st.from_user
JOIN users t ON t.id = st.to_user
WHERE st.group_id = $1 AND st.deleted_at IS NULL
ORDER BY st.settled_at;

-- name: CheckExportGroupMember :one
SELECT EXISTS(SELECT 1 FROM group_members WHERE group_id = $1 AND user_id = $2) AS is_member;

-- name: GetExportGroupName :one
SELECT name FROM groups WHERE id = $1;

-- ---------------------------------------------------------------- wallet export
-- Backup (GET /me/export.json, version 2) and CSV exports. Only live (not soft-deleted)
-- rows of the caller are exported; ids are kept as stored.

-- name: ExportUserProfile :one
SELECT id, name, email, avatar_url, default_currency, timezone, email_verified_at,
       created_at, updated_at, bank_name, payment_handle, bank_qr_url
FROM users WHERE id = $1;

-- name: ExportAccounts :many
SELECT * FROM accounts WHERE user_id = $1 AND deleted_at IS NULL ORDER BY sort_order, created_at, id;

-- name: ExportOwnedCategories :many
SELECT id, name, icon, color, grouping, created_at
FROM categories WHERE owner_id = $1 AND NOT is_system ORDER BY created_at, id;

-- name: ExportCategoryOrder :many
SELECT category_id, sort_order FROM category_order WHERE user_id = $1 ORDER BY sort_order, category_id;

-- name: ExportCategoryNames :many
SELECT id, name FROM categories WHERE id = ANY(@ids::uuid[]);

-- name: ExportTags :many
SELECT * FROM tags WHERE user_id = $1 ORDER BY sort_order, created_at, id;

-- name: ExportTransactionsPage :many
-- Keyset-paginated by id so large histories stream in bounded memory.
SELECT t.*,
       (SELECT COALESCE(array_agg(tt.tag_id ORDER BY tt.tag_id), '{}') FROM transaction_tags tt WHERE tt.transaction_id = t.id)::uuid[] AS tag_ids
FROM transactions t
WHERE t.user_id = @user_id AND t.deleted_at IS NULL AND t.id > @after_id::uuid
ORDER BY t.id
LIMIT @lim::int;

-- name: ExportTransactionsCSVPage :many
-- Newest first (same order as the ledger), keyset-paginated by (sort time, id).
SELECT t.id, t.type, t.title, t.description, t.amount, t.currency::text AS currency, t.to_amount,
       COALESCE(ta.currency::text, '')::text AS to_currency, t.occurred_at, t.due_at, t.source,
       COALESCE(a.name, '')::text AS account_name, COALESCE(ta.name, '')::text AS to_account_name,
       COALESCE(c.name, '')::text AS category_name,
       COALESCE((SELECT string_agg(g.name, '; ' ORDER BY g.sort_order, lower(g.name))
                 FROM transaction_tags tt JOIN tags g ON g.id = tt.tag_id WHERE tt.transaction_id = t.id), '')::text AS tag_names,
       COALESCE(t.occurred_at, t.due_at)::timestamptz AS sort_at
FROM transactions t
LEFT JOIN accounts a ON a.id = t.account_id
LEFT JOIN accounts ta ON ta.id = t.to_account_id
LEFT JOIN categories c ON c.id = t.category_id
WHERE t.user_id = @user_id AND t.deleted_at IS NULL
  AND (sqlc.narg(cursor_at)::timestamptz IS NULL
       OR (COALESCE(t.occurred_at, t.due_at), t.id) < (sqlc.narg(cursor_at)::timestamptz, @cursor_id::uuid))
ORDER BY COALESCE(t.occurred_at, t.due_at) DESC, t.id DESC
LIMIT @lim::int;

-- name: ExportPlannedPayments :many
SELECT p.*,
       (SELECT COALESCE(array_agg(pt.tag_id ORDER BY pt.tag_id), '{}') FROM planned_payment_tags pt WHERE pt.planned_payment_id = p.id)::uuid[] AS tag_ids
FROM planned_payments p
WHERE p.user_id = $1 AND p.deleted_at IS NULL
ORDER BY p.created_at, p.id;

-- name: ExportBudgets :many
SELECT * FROM budgets WHERE user_id = $1 ORDER BY sort_order, created_at, id;

-- name: ExportLoans :many
SELECT * FROM loans WHERE user_id = $1 AND deleted_at IS NULL ORDER BY sort_order, created_at, id;

-- name: ExportLoanRecords :many
SELECT r.* FROM loan_records r JOIN loans l ON l.id = r.loan_id
WHERE r.user_id = $1 AND r.deleted_at IS NULL AND l.deleted_at IS NULL
ORDER BY r.occurred_at, r.id;

-- name: ExportMyGroups :many
SELECT g.id, g.name, g.description, g.currency, g.group_type, g.archived_at, g.created_at, gm.role
FROM groups g JOIN group_members gm ON gm.group_id = g.id AND gm.user_id = $1
ORDER BY g.created_at, g.id;

-- name: ExportMyGroupExpenses :many
-- paid_by_id/payer_account_id and my_share describe the caller's side; payer_account_id is
-- only set when the caller paid.
SELECT e.id, e.group_id, e.description, e.category_id, coalesce(c.name, '')::text AS category,
       e.paid_by AS paid_by_id, pu.name AS paid_by, e.amount, e.currency::text AS currency, e.split_mode,
       e.expense_date, e.created_at, e.notes,
       (CASE WHEN e.paid_by = @user_id THEN e.payer_account_id END)::uuid AS payer_account_id,
       coalesce(s.amount, 0)::bigint AS my_share
FROM expenses e
JOIN group_members gm ON gm.group_id = e.group_id AND gm.user_id = @user_id
JOIN users pu ON pu.id = e.paid_by
LEFT JOIN categories c ON c.id = e.category_id
LEFT JOIN expense_splits s ON s.expense_id = e.id AND s.user_id = @user_id
WHERE e.deleted_at IS NULL
ORDER BY e.expense_date, e.created_at, e.id;

-- name: ExportMyGroupSettlements :many
SELECT st.id, st.group_id, st.from_user, f.name AS from_name, st.to_user, t.name AS to_name,
       st.amount, st.currency::text AS currency, st.note, st.settled_at,
       (CASE WHEN st.from_user = @user_id THEN st.from_account_id END)::uuid AS from_account_id
FROM settlements st
JOIN group_members gm ON gm.group_id = st.group_id AND gm.user_id = @user_id
JOIN users f ON f.id = st.from_user
JOIN users t ON t.id = st.to_user
WHERE st.deleted_at IS NULL
ORDER BY st.settled_at, st.id;
