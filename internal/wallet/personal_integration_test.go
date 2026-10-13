package wallet_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"github.com/settlr-org/settlr-api/internal/testutil"
)

func keys(m map[string]any) string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return strings.Join(out, ",")
}

const personalItemKeys = "amount,base_amount,base_currency,category_id,created_at,currency,description,exchange_rate,expense_date,id,notes"

// The legacy /personal API keeps its request/response shapes on top of transactions.
func TestIntegration_PersonalCompatOverTransactions(t *testing.T) {
	e := newEnv(t, testutil.CleanDB(t))
	alice := e.register("Alice", "alice-wallet-personal@test.local")
	mallory := e.register("Mallory", "mallory-wallet-personal@test.local")
	food := e.systemCategory("Food")

	created := e.must(201, "POST", "/api/v1/personal/expenses", alice, map[string]any{
		"description": "Lunch", "amount": 1200, "category_id": food, "notes": "momo", "expense_date": "2026-09-05",
	})
	if keys(created) != "amount,category_id,currency,description,expense_date,id,notes" || str(created["currency"]) != "NPR" || str(created["expense_date"]) != "2026-09-05" {
		t.Fatalf("create shape: %v", created)
	}
	id := str(created["id"])

	// The expense lives on a lazily created default "Cash" account in the user's currency.
	accs := e.accountsByName(alice)
	if len(accs) != 1 || accs["Cash"]["is_default"] != true || num(accs["Cash"]["balance"]) != -1200 {
		t.Fatalf("lazy cash account: %v", accs)
	}
	tx := e.must(200, "GET", "/api/v1/transactions/"+id, alice, nil)
	if str(tx["type"]) != "EXPENSE" || str(tx["title"]) != "Lunch" || str(tx["description"]) != "momo" || str(tx["source"]) != "PERSONAL_LEGACY" {
		t.Fatalf("backing transaction: %v", tx)
	}

	got := e.must(200, "GET", "/api/v1/personal/expenses/"+id, alice, nil)
	if keys(got) != personalItemKeys || str(got["notes"]) != "momo" || got["base_amount"] != nil {
		t.Fatalf("get shape: %v", got)
	}
	upd := e.must(200, "PATCH", "/api/v1/personal/expenses/"+id, alice, map[string]any{"description": "Dinner", "amount": 1500, "notes": "", "expense_date": "2026-09-06", "category_id": ""})
	if str(upd["description"]) != "Dinner" || num(upd["amount"]) != 1500 || str(upd["expense_date"]) != "2026-09-06" || upd["category_id"] != nil || keys(upd) != personalItemKeys {
		t.Fatalf("patch: %v", upd)
	}
	// Changing currency moves the expense to an account in that currency.
	upd = e.must(200, "PATCH", "/api/v1/personal/expenses/"+id, alice, map[string]any{"currency": "USD"})
	if str(upd["currency"]) != "USD" {
		t.Fatalf("currency patch: %v", upd)
	}
	if a := e.accountsByName(alice)["Cash USD"]; a == nil || num(a["balance"]) != -1500 {
		t.Fatalf("currency move: %v", e.accountsByName(alice))
	}
	e.must(201, "POST", "/api/v1/personal/expenses", alice, map[string]any{"description": "Taxi", "amount": 300, "currency": "NPR"})

	// Wallet expenses show up in the personal list; balance adjustments do not.
	cashID := str(e.accountsByName(alice)["Cash"]["id"])
	e.must(201, "POST", "/api/v1/transactions", alice, map[string]any{"type": "EXPENSE", "account_id": cashID, "amount": 50, "title": "Tea"})
	e.must(200, "POST", "/api/v1/accounts/"+cashID+"/adjust", alice, map[string]any{"balance": -10000})
	e.must(201, "POST", "/api/v1/transactions", alice, map[string]any{"type": "INCOME", "account_id": cashID, "amount": 50, "title": "Gift"})

	items := list(e.must(200, "GET", "/api/v1/personal/expenses", alice, nil), "data")
	if len(items) != 3 {
		t.Fatalf("personal list: %v", items)
	}
	for _, it := range items {
		if keys(it) != personalItemKeys {
			t.Fatalf("list item shape: %v", it)
		}
	}
	if got := list(e.must(200, "GET", "/api/v1/personal/expenses?q=taxi", alice, nil), "data"); len(got) != 1 {
		t.Fatalf("personal search: %v", got)
	}
	stats := e.must(200, "GET", "/api/v1/personal/stats", alice, nil)
	if num(stats["total"]) != 1500+300+50 || keys(stats) != "by_category,by_month,total" {
		t.Fatalf("stats: %v", stats)
	}
	status, csv := e.raw("GET", "/api/v1/personal/export.csv", alice.token, nil, nil)
	if status != 200 || !strings.HasPrefix(string(csv), "description,amount_cents,currency,date,notes\n") || strings.Count(string(csv), "\n") != 4 {
		t.Fatalf("export: %d %q", status, csv)
	}

	e.status(404, "GET", "/api/v1/personal/expenses/"+id, mallory, nil)
	e.status(404, "PATCH", "/api/v1/personal/expenses/"+id, mallory, map[string]any{"amount": 1})
	e.status(200, "DELETE", "/api/v1/personal/expenses/"+id, mallory, nil) // legacy: always 200
	e.status(200, "GET", "/api/v1/personal/expenses/"+id, alice, nil)
	e.status(422, "POST", "/api/v1/personal/expenses", alice, map[string]any{"description": "x", "amount": 1, "currency": "XYZ"})
	e.status(422, "POST", "/api/v1/personal/expenses", alice, map[string]any{"description": "", "amount": 1})

	// The compat DELETE never touches planned transactions.
	plannedTx := e.must(201, "POST", "/api/v1/transactions", alice, map[string]any{"type": "EXPENSE", "account_id": cashID, "amount": 900, "due_at": "2030-01-01"})
	e.must(200, "DELETE", "/api/v1/personal/expenses/"+str(plannedTx["id"]), alice, nil)
	e.status(200, "GET", "/api/v1/transactions/"+str(plannedTx["id"]), alice, nil)

	e.must(200, "DELETE", "/api/v1/personal/expenses/"+id, alice, nil)
	e.status(404, "GET", "/api/v1/personal/expenses/"+id, alice, nil)
	e.status(404, "GET", "/api/v1/transactions/"+id, alice, nil)

	// Budget endpoints are unchanged.
	e.must(200, "PUT", "/api/v1/personal/budget?month=2026-09", alice, map[string]any{"amount": 5000, "currency": "NPR"})
	if b := e.must(200, "GET", "/api/v1/personal/budget?month=2026-09", alice, nil); num(b["amount"]) != 5000 {
		t.Fatalf("budget: %v", b)
	}
}

// Concurrent first-time /personal writes must not race on the default account or
// create duplicate per-currency accounts.
func TestIntegration_PersonalConcurrentAccountBootstrap(t *testing.T) {
	e := newEnv(t, testutil.CleanDB(t))
	alice := e.register("Alice", "alice-wallet-race@test.local")
	const n = 12
	statuses := make(chan int, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			body := map[string]any{"description": "Race", "amount": 100 + i}
			if i%2 == 1 {
				body["currency"] = "USD"
			}
			b, _ := json.Marshal(body)
			req, _ := http.NewRequest("POST", e.srv.URL+"/api/v1/personal/expenses", bytes.NewReader(b))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", "Bearer "+alice.token)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				statuses <- 0
				return
			}
			resp.Body.Close()
			statuses <- resp.StatusCode
		}(i)
	}
	wg.Wait()
	close(statuses)
	for s := range statuses {
		if s != 201 {
			t.Fatalf("concurrent create: status %d", s)
		}
	}
	accs := list(e.must(200, "GET", "/api/v1/accounts", alice, nil), "data")
	if len(accs) != 2 {
		t.Fatalf("want exactly Cash + Cash USD, got %v", accs)
	}
	byName := e.accountsByName(alice)
	if byName["Cash"]["is_default"] != true || byName["Cash USD"] == nil {
		t.Fatalf("accounts: %v", byName)
	}
	if items := list(e.must(200, "GET", "/api/v1/personal/expenses", alice, nil), "data"); len(items) != n {
		t.Fatalf("expenses: %d", len(items))
	}
}

// Migration 0013 moves live personal_expenses into transactions on per-user Cash
// accounts, and its Down copies legacy-API changes back.
func TestIntegration_MigrationOfLegacyPersonalRows(t *testing.T) {
	pool := testutil.CleanDB(t)
	ctx := context.Background()
	sqlDB := stdlib.OpenDBFromPool(pool)
	provider, err := goose.NewProvider(goose.DialectPostgres, sqlDB, os.DirFS("../../migrations"))
	if err != nil {
		t.Fatal(err)
	}
	// Whatever happens, leave the shared test database fully migrated.
	t.Cleanup(func() {
		defer sqlDB.Close()
		if _, err := provider.Up(ctx); err != nil {
			t.Errorf("re-migrate: %v", err)
		}
	})
	if _, err := provider.DownTo(ctx, 12); err != nil {
		t.Fatalf("down to 12: %v", err)
	}

	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	exec(`INSERT INTO users (id, name, email, password_hash, default_currency) VALUES
		('00000000-0000-0000-0000-0000000000a1', 'Legacy A', 'legacy-a@test.local', 'x', 'NPR'),
		('00000000-0000-0000-0000-0000000000b2', 'Legacy B', 'legacy-b@test.local', 'x', 'USD'),
		('00000000-0000-0000-0000-0000000000c3', 'Legacy C', 'legacy-c@test.local', 'x', 'NPR')`)
	exec(`INSERT INTO personal_expenses (id, user_id, description, amount, currency, notes, expense_date, deleted_at) VALUES
		('00000000-0000-0000-0000-000000000001', '00000000-0000-0000-0000-0000000000a1', 'Rice', 1000, 'NPR', 'big bag', '2026-01-15', NULL),
		('00000000-0000-0000-0000-000000000002', '00000000-0000-0000-0000-0000000000a1', 'Book', 2500, 'USD', '', '2026-02-01', NULL),
		('00000000-0000-0000-0000-000000000003', '00000000-0000-0000-0000-0000000000a1', 'Gone', 99, 'NPR', '', '2026-02-02', now()),
		('00000000-0000-0000-0000-000000000004', '00000000-0000-0000-0000-0000000000b2', 'Bus', 300, 'USD', 'n', '2026-03-03', NULL),
		('00000000-0000-0000-0000-000000000005', '00000000-0000-0000-0000-0000000000c3', 'Old', 5, 'NPR', '', '2026-03-03', now())`)

	if _, err := provider.UpTo(ctx, 13); err != nil {
		t.Fatalf("up to 13: %v", err)
	}

	type acct struct {
		name, currency string
		isDefault      bool
	}
	accountsOf := func(user string) []acct {
		rows, err := pool.Query(ctx, `SELECT name, currency, is_default FROM accounts WHERE user_id = $1 ORDER BY sort_order`, user)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var out []acct
		for rows.Next() {
			var a acct
			if err := rows.Scan(&a.name, &a.currency, &a.isDefault); err != nil {
				t.Fatal(err)
			}
			out = append(out, a)
		}
		return out
	}
	if got := accountsOf("00000000-0000-0000-0000-0000000000a1"); len(got) != 2 || got[0] != (acct{"Cash", "NPR", true}) || got[1] != (acct{"Cash USD", "USD", false}) {
		t.Fatalf("user A accounts: %v", got)
	}
	if got := accountsOf("00000000-0000-0000-0000-0000000000b2"); len(got) != 1 || got[0] != (acct{"Cash", "USD", true}) {
		t.Fatalf("user B accounts: %v", got)
	}
	if got := accountsOf("00000000-0000-0000-0000-0000000000c3"); len(got) != 0 {
		t.Fatalf("user with only deleted rows should get no account: %v", got)
	}

	var n int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM transactions`).Scan(&n)
	if n != 3 {
		t.Fatalf("migrated transactions: %d", n)
	}
	var typ, title, desc, src, cur, accName, date string
	var amount int64
	if err := pool.QueryRow(ctx, `SELECT t.type, t.title, t.description, t.source, t.currency, a.name, t.amount, to_char(t.occurred_at AT TIME ZONE 'UTC', 'YYYY-MM-DD')
		FROM transactions t JOIN accounts a ON a.id = t.account_id WHERE t.id = '00000000-0000-0000-0000-000000000002'`).
		Scan(&typ, &title, &desc, &src, &cur, &accName, &amount, &date); err != nil {
		t.Fatal(err)
	}
	if typ != "EXPENSE" || title != "Book" || src != "PERSONAL_LEGACY" || cur != "USD" || accName != "Cash USD" || amount != 2500 || date != "2026-02-01" {
		t.Fatalf("migrated row: %s %s %s %s %s %d %s", typ, title, src, cur, accName, amount, date)
	}
	_ = pool.QueryRow(ctx, `SELECT description FROM transactions WHERE id = '00000000-0000-0000-0000-000000000001'`).Scan(&desc)
	if desc != "big bag" {
		t.Fatalf("notes -> description: %q", desc)
	}

	// Changes made through the new layer survive a rollback of the migration.
	exec(`UPDATE transactions SET amount = 1111, title = 'Rice 2' WHERE id = '00000000-0000-0000-0000-000000000001'`)
	exec(`UPDATE transactions SET deleted_at = now() WHERE id = '00000000-0000-0000-0000-000000000004'`)
	exec(`INSERT INTO transactions (id, user_id, type, account_id, amount, currency, title, occurred_at, source)
		SELECT '00000000-0000-0000-0000-000000000009', user_id, 'EXPENSE', id, 70, currency, 'New', '2026-04-04', 'PERSONAL_LEGACY'
		FROM accounts WHERE user_id = '00000000-0000-0000-0000-0000000000a1' AND is_default`)
	var deleted bool
	// Legacy rows that stopped being live expenses must not come back as live ones.
	exec(`UPDATE transactions SET type = 'INCOME' WHERE id = '00000000-0000-0000-0000-000000000002'`)
	exec(`INSERT INTO transactions (id, user_id, type, account_id, amount, currency, title, due_at, source)
		SELECT '00000000-0000-0000-0000-00000000000a', user_id, 'EXPENSE', id, 80, currency, 'Planned', '2026-12-01', 'PERSONAL_LEGACY'
		FROM accounts WHERE user_id = '00000000-0000-0000-0000-0000000000a1' AND is_default`)
	// Wallet-only data is discarded.
	exec(`INSERT INTO transactions (id, user_id, type, account_id, amount, currency, title, occurred_at, source)
		SELECT '00000000-0000-0000-0000-00000000000b', user_id, 'EXPENSE', id, 90, currency, 'Manual', '2026-04-05', 'MANUAL'
		FROM accounts WHERE user_id = '00000000-0000-0000-0000-0000000000a1' AND is_default`)
	if _, err := provider.DownTo(ctx, 12); err != nil {
		t.Fatalf("down to 12 again: %v", err)
	}
	for _, id := range []string{"00000000-0000-0000-0000-000000000002", "00000000-0000-0000-0000-00000000000a"} {
		if err := pool.QueryRow(ctx, `SELECT deleted_at IS NOT NULL FROM personal_expenses WHERE id = $1`, id).Scan(&deleted); err != nil || !deleted {
			t.Fatalf("non-live legacy row %s should be copied back deleted (err=%v)", id, err)
		}
	}
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM personal_expenses WHERE id = '00000000-0000-0000-0000-00000000000b'`).Scan(&n)
	if n != 0 {
		t.Fatal("down must not copy wallet-only transactions")
	}
	var amt int64
	_ = pool.QueryRow(ctx, `SELECT amount, description FROM personal_expenses WHERE id = '00000000-0000-0000-0000-000000000001'`).Scan(&amt, &title)
	if amt != 1111 || title != "Rice 2" {
		t.Fatalf("down did not copy edits: %d %s", amt, title)
	}
	_ = pool.QueryRow(ctx, `SELECT deleted_at IS NOT NULL FROM personal_expenses WHERE id = '00000000-0000-0000-0000-000000000004'`).Scan(&deleted)
	if !deleted {
		t.Fatal("down did not copy delete")
	}
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM personal_expenses WHERE id = '00000000-0000-0000-0000-000000000009'`).Scan(&n)
	if n != 1 {
		t.Fatal("down did not copy new expense")
	}
	var exists bool
	_ = pool.QueryRow(ctx, `SELECT to_regclass('public.transactions') IS NOT NULL`).Scan(&exists)
	if exists {
		t.Fatal("down should drop transactions")
	}
	_ = pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM schema_migrations WHERE version = '0013_wallet_foundation')`).Scan(&exists)
	if exists {
		t.Fatal("down should forget the legacy schema_migrations row")
	}
}
