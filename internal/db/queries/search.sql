-- Search domain queries for sqlc with pgx/v5.
-- These replace manual Pool.Query/Exec calls in internal/search/handlers.go
-- Handler usage: Handler{Pool *pgxpool.Pool, Queries *db.Queries} where Queries wraps the pool.

-- name: SearchUsers :many
SELECT id, name, coalesce(avatar_url, '') AS avatar_url
FROM users
WHERE is_anonymous = false
  AND (name ILIKE '%' || $1 || '%' OR email ILIKE '%' || $1 || '%')
LIMIT 10;

-- name: SearchGroups :many
SELECT g.id, g.name
FROM groups g
JOIN group_members gm ON gm.group_id = g.id
WHERE gm.user_id = $1
  AND g.archived_at IS NULL
  AND g.name ILIKE '%' || $2 || '%'
LIMIT 10;

-- name: SearchExpenses :many
SELECT e.id, e.group_id, e.description, e.amount, e.currency
FROM expenses e
WHERE e.deleted_at IS NULL
  AND e.group_id IN (SELECT group_id FROM group_members WHERE user_id = $1)
  AND (e.description ILIKE '%' || $2 || '%' OR e.notes ILIKE '%' || $2 || '%')
ORDER BY e.created_at DESC
LIMIT 10;

-- ---------------------------------------------------------------- wallet search
-- Patterns are built by the handler with %, _ and \ escaped; matching is ILIKE ... ESCAPE '\'.

-- name: SearchWalletTransactions :many
-- Planned items are included; balance adjustments and loan bookings are not. The
-- three match sources are UNIONed (instead of OR-ed) so each can use the trigram
-- indexes (0016) or the per-user index, whichever the planner finds cheaper.
WITH m AS (
    SELECT t1.id FROM transactions t1
    WHERE t1.user_id = @user_id AND t1.deleted_at IS NULL AND t1.title ILIKE @pattern::text ESCAPE '\'
    UNION
    SELECT t2.id FROM transactions t2
    WHERE t2.user_id = @user_id AND t2.deleted_at IS NULL AND t2.description ILIKE @pattern::text ESCAPE '\'
    UNION
    SELECT tt.transaction_id FROM transaction_tags tt JOIN tags g ON g.id = tt.tag_id
    WHERE g.user_id = @user_id AND g.name ILIKE @pattern::text ESCAPE '\'
)
SELECT t.id, t.type, t.title, t.description, t.amount, t.currency::text AS currency,
       t.account_id, a.name AS account_name, t.category_id, t.occurred_at, t.due_at, t.source
FROM transactions t
JOIN m ON m.id = t.id
LEFT JOIN accounts a ON a.id = t.account_id
WHERE t.user_id = @user_id AND t.deleted_at IS NULL
  AND t.source NOT IN ('ADJUSTMENT', 'LOAN')
ORDER BY COALESCE(t.occurred_at, t.due_at) DESC, t.id DESC
LIMIT 10;

-- name: SearchWalletAccounts :many
SELECT id, name, currency::text AS currency, color, icon
FROM accounts
WHERE user_id = @user_id AND deleted_at IS NULL AND name ILIKE @pattern::text ESCAPE '\'
ORDER BY sort_order, name, id
LIMIT 10;

-- name: SearchWalletLoans :many
SELECT id, name, type, amount, currency::text AS currency
FROM loans
WHERE user_id = @user_id AND deleted_at IS NULL
  AND (name ILIKE @pattern::text ESCAPE '\' OR note ILIKE @pattern::text ESCAPE '\')
ORDER BY sort_order, name, id
LIMIT 10;

-- name: SearchWalletPlanned :many
SELECT p.id, p.title, p.type, p.amount, p.currency::text AS currency, p.one_time, p.interval_n, p.interval_unit, p.active,
       (SELECT t.due_at FROM transactions t
         WHERE t.planned_payment_id = p.id AND t.occurred_at IS NULL AND t.deleted_at IS NULL
         LIMIT 1) AS next_due_at
FROM planned_payments p
WHERE p.user_id = @user_id AND p.deleted_at IS NULL
  AND (p.title ILIKE @pattern::text ESCAPE '\' OR p.description ILIKE @pattern::text ESCAPE '\'
       OR EXISTS (SELECT 1 FROM planned_payment_tags pt JOIN tags g ON g.id = pt.tag_id
                  WHERE pt.planned_payment_id = p.id AND g.name ILIKE @pattern::text ESCAPE '\'))
ORDER BY p.active DESC, p.created_at, p.id
LIMIT 10;

-- name: SearchWalletBudgets :many
SELECT id, name, amount, currency::text AS currency
FROM budgets
WHERE user_id = @user_id AND name ILIKE @pattern::text ESCAPE '\'
ORDER BY sort_order, name, id
LIMIT 10;

-- name: SearchWalletCategories :many
SELECT id, name, icon, color
FROM categories
WHERE (is_system OR owner_id = @user_id) AND name ILIKE @pattern::text ESCAPE '\'
ORDER BY is_system, name, id
LIMIT 10;

-- name: SearchWalletTags :many
SELECT id, name, color
FROM tags
WHERE user_id = @user_id AND name ILIKE @pattern::text ESCAPE '\'
ORDER BY sort_order, lower(name), id
LIMIT 10;
