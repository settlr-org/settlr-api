package export_test

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/settlr-org/settlr-api/internal/auth"
	"github.com/settlr-org/settlr-api/internal/categories"
	"github.com/settlr-org/settlr-api/internal/config"
	db "github.com/settlr-org/settlr-api/internal/db"
	"github.com/settlr-org/settlr-api/internal/expenses"
	"github.com/settlr-org/settlr-api/internal/export"
	"github.com/settlr-org/settlr-api/internal/groups"
	"github.com/settlr-org/settlr-api/internal/rates"
	"github.com/settlr-org/settlr-api/internal/testutil"
	"github.com/settlr-org/settlr-api/internal/wallet"
)

type env struct {
	t    *testing.T
	srv  *httptest.Server
	pool *pgxpool.Pool
}

type user struct {
	id    uuid.UUID
	token string
}

type noRates struct{}

func (noRates) Latest(context.Context, string) (map[string]float64, error) {
	return nil, fmt.Errorf("offline")
}

func newEnv(t *testing.T) *env {
	t.Helper()
	pool := testutil.CleanDB(t)
	cfg := config.Config{
		JWTSecret: "test-jwt-secret-32-chars-min!!!!", JWTRefreshSecret: "test-refresh-secret-32-chars!!",
		JWTExpiryMinutes: 15, RefreshExpiryDays: 30, Env: "test",
	}
	authSvc := &auth.Service{Pool: pool, Cfg: cfg}
	mux := http.NewServeMux()
	(&auth.Handler{Svc: authSvc}).RegisterRoutes(mux)
	authMw := auth.Middleware(authSvc)
	queries := db.New(pool)
	(&groups.Handler{Pool: pool}).RegisterRoutes(mux, authMw)
	(&categories.Handler{Pool: pool}).RegisterRoutes(mux, authMw)
	(&expenses.Handler{Pool: pool}).RegisterRoutes(mux, authMw)
	(&wallet.Handler{Pool: pool, Queries: queries, Rates: rates.NewConverter(noRates{})}).RegisterRoutes(mux, authMw)
	(&export.Handler{Pool: pool, Queries: queries}).RegisterRoutes(mux, authMw)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &env{t: t, srv: srv, pool: pool}
}

func (e *env) raw(method, path, token string, body any) (int, http.Header, []byte) {
	e.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, e.srv.URL+path, rd)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, resp.Header, b
}

func (e *env) must(want int, method, path string, u user, body any) map[string]any {
	e.t.Helper()
	status, _, raw := e.raw(method, path, u.token, body)
	if status != want {
		e.t.Fatalf("%s %s: status %d want %d: %s", method, path, status, want, raw)
	}
	out := map[string]any{}
	_ = json.Unmarshal(raw, &out)
	return out
}

func (e *env) register(name, email string) user {
	e.t.Helper()
	_, _, raw := e.raw("POST", "/api/v1/auth/register", "", map[string]string{"name": name, "email": email, "password": "password123"})
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		e.t.Fatalf("register: %s", raw)
	}
	id, _ := uuid.Parse(out["user"].(map[string]any)["id"].(string))
	return user{id: id, token: out["access_token"].(string)}
}

func str(v any) string { s, _ := v.(string); return s }

func arr(m map[string]any, k string) []map[string]any {
	a, _ := m[k].([]any)
	out := make([]map[string]any, 0, len(a))
	for _, x := range a {
		out = append(out, x.(map[string]any))
	}
	return out
}

func ids(rows []map[string]any) map[string]bool {
	out := map[string]bool{}
	for _, r := range rows {
		out[str(r["id"])] = true
	}
	return out
}

func (e *env) exportJSON(u user) (map[string]any, []byte) {
	e.t.Helper()
	status, hdr, raw := e.raw("GET", "/api/v1/me/export.json", u.token, nil)
	if status != 200 || !strings.HasPrefix(hdr.Get("Content-Type"), "application/json") || !strings.Contains(hdr.Get("Content-Disposition"), "attachment") {
		e.t.Fatalf("export.json: %d %v", status, hdr)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		e.t.Fatalf("export.json is not valid JSON: %v", err)
	}
	return doc, raw
}

func (e *env) exportCSV(u user, path string) [][]string {
	e.t.Helper()
	status, hdr, raw := e.raw("GET", path, u.token, nil)
	if status != 200 || !strings.HasPrefix(hdr.Get("Content-Type"), "text/csv") {
		e.t.Fatalf("%s: %d %v", path, status, hdr)
	}
	rows, err := csv.NewReader(bytes.NewReader(raw)).ReadAll()
	if err != nil {
		e.t.Fatalf("%s: invalid CSV: %v\n%s", path, err, raw)
	}
	return rows
}

func TestIntegration_BackupDocument(t *testing.T) {
	e := newEnv(t)
	alice := e.register("Alice Backup", "alice-backup@test.local")
	mallory := e.register("Mallory Backup", "mallory-backup@test.local")

	bank := e.must(201, "POST", "/api/v1/accounts", alice, map[string]any{"name": "Bank", "currency": "USD", "initial_balance": 100000})
	card := e.must(201, "POST", "/api/v1/accounts", alice, map[string]any{"name": "Card", "currency": "USD"})
	bankID, cardID := str(bank["id"]), str(card["id"])
	tag := e.must(201, "POST", "/api/v1/tags", alice, map[string]any{"name": "Trip", "color": "#ff0000"})
	cat := e.must(201, "POST", "/api/v1/categories", alice, map[string]any{"name": "Pets"})
	e.must(200, "PUT", "/api/v1/me/rates/USD/NPR", alice, map[string]any{"rate": 140.5})
	e.must(200, "PATCH", "/api/v1/me/settings", alice, map[string]any{"month_start_day": 5, "theme": "dark"})
	t1 := e.must(201, "POST", "/api/v1/transactions", alice, map[string]any{
		"type": "EXPENSE", "account_id": bankID, "amount": 1250, "title": "Dog food", "description": "big bag",
		"occurred_at": "2026-06-01T10:00:00Z", "category_id": str(cat["id"]), "tag_ids": []string{str(tag["id"])},
	})
	e.must(201, "POST", "/api/v1/transactions", alice, map[string]any{
		"type": "TRANSFER", "account_id": bankID, "to_account_id": cardID, "amount": 500, "to_amount": 500, "occurred_at": "2026-06-02T10:00:00Z",
	})
	planned := e.must(201, "POST", "/api/v1/transactions", alice, map[string]any{"type": "EXPENSE", "account_id": bankID, "amount": 900, "title": "Insurance", "due_at": "2026-12-01"})
	gone := e.must(201, "POST", "/api/v1/transactions", alice, map[string]any{"type": "EXPENSE", "account_id": bankID, "amount": 1, "title": "Deleted one", "occurred_at": "2026-06-03T10:00:00Z"})
	e.must(200, "DELETE", "/api/v1/transactions/"+str(gone["id"]), alice, nil)
	if _, err := e.pool.Exec(context.Background(),
		`INSERT INTO transactions (user_id, type, account_id, amount, currency, title, occurred_at, source, import_ref)
		 VALUES ($1, 'INCOME', $2, 7700, 'USD', 'Imported salary', '2026-06-04T00:00:00Z', 'IMPORT', 'bank-csv:row-17')`, alice.id, bankID); err != nil {
		t.Fatal(err)
	}
	plan := e.must(201, "POST", "/api/v1/planned", alice, map[string]any{
		"type": "EXPENSE", "account_id": bankID, "amount": 500, "title": "Rent", "start_at": "2026-11-01", "interval_n": 1, "interval_unit": "MONTH",
		"tag_ids": []string{str(tag["id"])},
	})
	budget := e.must(201, "POST", "/api/v1/budgets", alice, map[string]any{"name": "Pets budget", "amount": 5000, "category_ids": []string{str(cat["id"])}, "account_ids": []string{bankID}})
	loan := e.must(201, "POST", "/api/v1/loans", alice, map[string]any{"name": "To Bob", "type": "LEND", "amount": 1000, "account_id": bankID})
	rec := e.must(201, "POST", "/api/v1/loans/"+str(loan["id"])+"/records", alice, map[string]any{"type": "DECREASE", "amount": 250})
	g := e.must(201, "POST", "/api/v1/groups", alice, map[string]any{"name": "Flat", "currency": "USD"})
	e.must(201, "POST", "/api/v1/groups/"+str(g["id"])+"/expenses", alice, map[string]any{
		"description": "Groceries", "amount": 4000, "paid_by": alice.id.String(), "split_mode": "EQUAL",
		"splits": []map[string]string{{"user_id": alice.id.String()}}, "expense_date": "2026-05-10", "payer_account_id": bankID,
	})

	// Mallory has data too; none of it may show up in Alice's export (and vice versa).
	mBank := str(e.must(201, "POST", "/api/v1/accounts", mallory, map[string]any{"name": "Mal Vault", "currency": "USD"})["id"])
	e.must(201, "POST", "/api/v1/transactions", mallory, map[string]any{"type": "EXPENSE", "account_id": mBank, "amount": 5, "title": "Mal secret", "occurred_at": "2026-06-01T10:00:00Z"})
	e.must(201, "POST", "/api/v1/tags", mallory, map[string]any{"name": "MalTag"})
	e.must(201, "POST", "/api/v1/loans", mallory, map[string]any{"name": "Mal loan", "type": "BORROW", "amount": 9})
	e.must(201, "POST", "/api/v1/budgets", mallory, map[string]any{"name": "Mal budget", "amount": 9})
	e.must(201, "POST", "/api/v1/categories", mallory, map[string]any{"name": "Mal category"})

	doc, raw := e.exportJSON(alice)
	if doc["version"] != float64(2) || str(doc["exported_at"]) == "" {
		t.Fatalf("version/exported_at: %v %v", doc["version"], doc["exported_at"])
	}
	for _, k := range []string{"user", "settings", "rate_overrides", "accounts", "categories", "tags", "transactions", "planned_payments", "budgets", "loans", "groups", "data"} {
		if _, ok := doc[k]; !ok {
			t.Fatalf("missing top-level key %q", k)
		}
	}
	// No secrets, none of Mallory's data.
	body := string(raw)
	for _, bad := range []string{"password", "hash", "refresh", "Mal secret", "Mal Vault", "MalTag", "Mal loan", "Mal budget", "Mal category", "mallory"} {
		if strings.Contains(strings.ToLower(body), strings.ToLower(bad)) {
			t.Fatalf("export contains %q", bad)
		}
	}
	u := doc["user"].(map[string]any)
	if str(u["id"]) != alice.id.String() || str(u["email"]) != "alice-backup@test.local" || str(u["default_currency"]) == "" {
		t.Fatalf("user: %v", u)
	}
	s := doc["settings"].(map[string]any)
	if s["month_start_day"] != float64(5) || str(s["theme"]) != "dark" {
		t.Fatalf("settings: %v", s)
	}
	if r := arr(doc, "rate_overrides"); len(r) != 1 || str(r[0]["base"]) != "USD" || str(r[0]["quote"]) != "NPR" || r[0]["rate"] != 140.5 {
		t.Fatalf("rate_overrides: %v", r)
	}
	accIDs := ids(arr(doc, "accounts"))
	if len(accIDs) != 2 || !accIDs[bankID] || !accIDs[cardID] {
		t.Fatalf("accounts: %v", arr(doc, "accounts"))
	}
	if c := arr(doc, "categories"); len(c) != 1 || str(c[0]["id"]) != str(cat["id"]) || str(c[0]["name"]) != "Pets" {
		t.Fatalf("categories (owned only): %v", c)
	}
	if tg := arr(doc, "tags"); len(tg) != 1 || str(tg[0]["id"]) != str(tag["id"]) {
		t.Fatalf("tags: %v", tg)
	}

	// Transactions: stable ids, planned included, soft-deleted excluded, tag_ids/import_ref/source present.
	txs := arr(doc, "transactions")
	byTitle := map[string]map[string]any{}
	for _, tx := range txs {
		byTitle[str(tx["title"])] = tx
		for _, k := range []string{"id", "type", "account_id", "to_account_id", "amount", "currency", "to_amount", "category_id", "title", "description",
			"occurred_at", "due_at", "planned", "planned_payment_id", "loan_id", "loan_record_id", "source", "import_ref", "tag_ids", "created_at", "updated_at"} {
			if _, ok := tx[k]; !ok {
				t.Fatalf("transaction missing %q: %v", k, tx)
			}
		}
		if a := str(tx["account_id"]); a != "" && !accIDs[a] {
			t.Fatalf("transaction references unknown account %s", a)
		}
	}
	if _, ok := byTitle["Deleted one"]; ok {
		t.Fatal("soft-deleted transaction exported")
	}
	if tx := byTitle["Dog food"]; str(tx["id"]) != str(t1["id"]) || tx["amount"] != float64(1250) || str(tx["category_id"]) != str(cat["id"]) ||
		len(tx["tag_ids"].([]any)) != 1 || tx["tag_ids"].([]any)[0] != str(tag["id"]) || tx["planned"] != false || str(tx["source"]) != "MANUAL" {
		t.Fatalf("Dog food: %v", tx)
	}
	if tx := byTitle["Insurance"]; str(tx["id"]) != str(planned["id"]) || tx["planned"] != true || tx["occurred_at"] != nil || str(tx["due_at"]) == "" {
		t.Fatalf("planned: %v", tx)
	}
	if tx := byTitle["Imported salary"]; str(tx["import_ref"]) != "bank-csv:row-17" || str(tx["source"]) != "IMPORT" {
		t.Fatalf("import: %v", tx)
	}
	var transfer map[string]any
	for _, tx := range txs {
		if tx["type"] == "TRANSFER" {
			transfer = tx
		}
	}
	if transfer == nil || str(transfer["to_account_id"]) != cardID || transfer["to_amount"] != float64(500) {
		t.Fatalf("transfer: %v", transfer)
	}
	hasSource := map[string]bool{}
	for _, tx := range txs {
		hasSource[str(tx["source"])] = true
	}
	if !hasSource["ADJUSTMENT"] || !hasSource["LOAN"] {
		t.Fatalf("adjustment/loan bookings must be part of the backup: %v", hasSource)
	}
	// Every API-visible transaction (planned included) is in the export, nothing more.
	api := e.must(200, "GET", "/api/v1/transactions?planned=all&limit=200", alice, nil)
	apiIDs := ids(arr(api, "data"))
	if len(apiIDs) != len(txs) {
		t.Fatalf("export has %d transactions, API %d", len(txs), len(apiIDs))
	}
	for id := range ids(txs) {
		if !apiIDs[id] {
			t.Fatalf("exported transaction %s unknown to API", id)
		}
	}

	pp := arr(doc, "planned_payments")
	if len(pp) != 1 || str(pp[0]["id"]) != str(plan["id"]) || str(pp[0]["interval_unit"]) != "MONTH" || pp[0]["interval_n"] != float64(1) ||
		len(pp[0]["tag_ids"].([]any)) != 1 || str(pp[0]["start_at"]) == "" || pp[0]["active"] != true {
		t.Fatalf("planned_payments: %v", pp)
	}
	if b := arr(doc, "budgets"); len(b) != 1 || str(b[0]["id"]) != str(budget["id"]) || len(b[0]["category_ids"].([]any)) != 1 || len(b[0]["account_ids"].([]any)) != 1 {
		t.Fatalf("budgets: %v", b)
	}
	ln := arr(doc, "loans")
	if len(ln) != 1 || str(ln[0]["id"]) != str(loan["id"]) || ln[0]["amount"] != float64(1000) {
		t.Fatalf("loans: %v", ln)
	}
	if rs := arr(ln[0], "records"); len(rs) != 1 || str(rs[0]["id"]) != str(rec["id"]) || rs[0]["amount"] != float64(250) || str(rs[0]["loan_id"]) != str(loan["id"]) {
		t.Fatalf("loan records: %v", rs)
	}
	gs := arr(doc, "groups")
	if len(gs) != 1 || str(gs[0]["id"]) != str(g["id"]) || len(arr(gs[0], "expenses")) != 1 {
		t.Fatalf("groups: %v", gs)
	}
	ge := arr(gs[0], "expenses")[0]
	if ge["my_share"] != float64(4000) || str(ge["payer_account_id"]) != bankID || str(ge["paid_by_id"]) != alice.id.String() {
		t.Fatalf("group expense: %v", ge)
	}
	// Version-1 key kept.
	if d := arr(doc, "data"); len(d) != 1 || str(d[0]["description"]) != "Groceries" || str(d[0]["group_name"]) != "Flat" || d[0]["amount"] != float64(4000) {
		t.Fatalf("legacy data: %v", d)
	}

	// Mallory's own export is separate and complete.
	mdoc, mraw := e.exportJSON(mallory)
	if len(arr(mdoc, "transactions")) != 2 /* initial-balance adjustment is skipped at 0 */ && len(arr(mdoc, "transactions")) != 1 {
		t.Fatalf("mallory transactions: %v", arr(mdoc, "transactions"))
	}
	for _, bad := range []string{"Dog food", "Imported salary", "Pets", "Alice", "alice-backup"} {
		if strings.Contains(string(mraw), bad) {
			t.Fatalf("mallory export contains %q", bad)
		}
	}

	// Auth required.
	if s, _, _ := e.raw("GET", "/api/v1/me/export.json", "", nil); s != 401 {
		t.Fatalf("unauthenticated: %d", s)
	}

	// An empty account still yields a complete document with empty arrays.
	bob := e.register("Bob Empty", "bob-empty@test.local")
	edoc, _ := e.exportJSON(bob)
	for _, k := range []string{"rate_overrides", "accounts", "categories", "tags", "transactions", "planned_payments", "budgets", "loans", "groups", "data"} {
		if a, ok := edoc[k].([]any); !ok || len(a) != 0 {
			t.Fatalf("empty export %s: %v", k, edoc[k])
		}
	}
}

// Large histories stream in several pages; the document stays valid and complete.
func TestIntegration_ExportStreamsLargeHistories(t *testing.T) {
	e := newEnv(t)
	u := e.register("Big Spender", "big-export@test.local")
	acc := str(e.must(201, "POST", "/api/v1/accounts", u, map[string]any{"name": "Main", "currency": "USD"})["id"])
	const n = 2350
	// Many rows share a timestamp (hourly buckets) to exercise cursor tie-breaking.
	if _, err := e.pool.Exec(context.Background(), `
		INSERT INTO transactions (user_id, type, account_id, amount, currency, title, occurred_at)
		SELECT $1, 'EXPENSE', $2, 100 + g, 'USD', 'Row ' || g, '2026-01-01T00:00:00Z'::timestamptz + (g / 7 || ' hours')::interval
		FROM generate_series(1, $3) g`, u.id, acc, n); err != nil {
		t.Fatal(err)
	}
	doc, _ := e.exportJSON(u)
	txs := arr(doc, "transactions")
	if len(txs) != n || len(ids(txs)) != n {
		t.Fatalf("json: %d rows, %d unique, want %d", len(txs), len(ids(txs)), n)
	}
	rows := e.exportCSV(u, "/api/v1/me/export/transactions.csv")
	if len(rows) != n+1 {
		t.Fatalf("csv: %d rows, want %d", len(rows), n+1)
	}
	seen := map[string]bool{}
	for _, r := range rows[1:] {
		if seen[r[0]] {
			t.Fatalf("csv duplicate id %s", r[0])
		}
		seen[r[0]] = true
	}
	for i := 2; i < len(rows); i++ {
		if rows[i][1] > rows[i-1][1] {
			t.Fatalf("csv not newest-first at row %d: %s then %s", i, rows[i-1][1], rows[i][1])
		}
	}
}

func TestIntegration_ExportCSVs(t *testing.T) {
	e := newEnv(t)
	alice := e.register("Alice Csv", "alice-csv@test.local")
	mallory := e.register("Mallory Csv", "mallory-csv@test.local")
	bank := str(e.must(201, "POST", "/api/v1/accounts", alice, map[string]any{"name": "=cmd|' /C calc'!A0", "currency": "USD"})["id"])
	savings := str(e.must(201, "POST", "/api/v1/accounts", alice, map[string]any{"name": "Savings, \"main\"", "currency": "EUR"})["id"])
	tag1 := e.must(201, "POST", "/api/v1/tags", alice, map[string]any{"name": "Trip"})
	tag2 := e.must(201, "POST", "/api/v1/tags", alice, map[string]any{"name": "@work"})
	cat := e.must(201, "POST", "/api/v1/categories", alice, map[string]any{"name": "Pets"})
	tx := func(body map[string]any) { e.must(201, "POST", "/api/v1/transactions", alice, body) }
	tx(map[string]any{"type": "EXPENSE", "account_id": bank, "amount": 12345, "title": "+1 pizza, \"large\"", "description": "line1\nline2", "occurred_at": "2026-06-01T10:00:00Z",
		"category_id": str(cat["id"]), "tag_ids": []string{str(tag1["id"]), str(tag2["id"])}})
	tx(map[string]any{"type": "TRANSFER", "account_id": bank, "to_account_id": savings, "amount": 1000, "to_amount": 900, "occurred_at": "2026-06-02T10:00:00Z", "title": "-move"})
	tx(map[string]any{"type": "INCOME", "account_id": bank, "amount": 5, "title": "@sum", "due_at": "2026-12-01"})
	tx(map[string]any{"type": "EXPENSE", "account_id": bank, "amount": 1, "title": "plain", "occurred_at": "2026-06-03T10:00:00Z"})
	e.must(201, "POST", "/api/v1/loans", alice, map[string]any{"name": "=SUM(A1)", "type": "LEND", "amount": 1000, "note": "owes me"})
	e.must(201, "POST", "/api/v1/budgets", alice, map[string]any{"name": "Pets budget", "amount": 5000, "category_ids": []string{str(cat["id"])}, "account_ids": []string{bank}})

	mBank := str(e.must(201, "POST", "/api/v1/accounts", mallory, map[string]any{"name": "Mal Vault", "currency": "USD"})["id"])
	e.must(201, "POST", "/api/v1/transactions", mallory, map[string]any{"type": "EXPENSE", "account_id": mBank, "amount": 5, "title": "Mal secret", "occurred_at": "2026-06-01T10:00:00Z"})
	e.must(201, "POST", "/api/v1/loans", mallory, map[string]any{"name": "Mal loan", "type": "BORROW", "amount": 9})
	e.must(201, "POST", "/api/v1/budgets", mallory, map[string]any{"name": "Mal budget", "amount": 9})

	rows := e.exportCSV(alice, "/api/v1/me/export/transactions.csv")
	hdr := strings.Join(rows[0], ",")
	if hdr != "id,date,planned,type,title,description,account,to_account,category,tags,amount_minor,amount,currency,to_amount_minor,to_amount,to_currency,source" {
		t.Fatalf("header: %s", hdr)
	}
	col := func(name string) int {
		for i, h := range rows[0] {
			if h == name {
				return i
			}
		}
		t.Fatalf("no column %s", name)
		return -1
	}
	byTitle := map[string][]string{}
	for _, r := range rows[1:] {
		byTitle[r[col("title")]] = r
		if strings.Contains(strings.Join(r, "|"), "Mal") {
			t.Fatalf("foreign data in CSV: %v", r)
		}
	}
	pizza := byTitle["'+1 pizza, \"large\""]
	if pizza == nil {
		t.Fatalf("injection-protected, correctly quoted title missing: %v", byTitle)
	}
	if pizza[col("description")] != "line1\nline2" || pizza[col("account")] != "'=cmd|' /C calc'!A0" || pizza[col("category")] != "Pets" ||
		pizza[col("tags")] != "Trip; @work" || pizza[col("amount_minor")] != "12345" ||
		pizza[col("amount")] != "123.45" || pizza[col("currency")] != "USD" || pizza[col("planned")] != "false" || pizza[col("type")] != "EXPENSE" ||
		pizza[col("source")] != "MANUAL" || pizza[col("date")] != "2026-06-01T10:00:00Z" {
		t.Fatalf("pizza row: %v", pizza)
	}
	mv := byTitle["'-move"]
	if mv == nil || mv[col("type")] != "TRANSFER" || mv[col("to_account")] != "Savings, \"main\"" || mv[col("to_amount_minor")] != "900" ||
		mv[col("to_amount")] != "9.00" || mv[col("to_currency")] != "EUR" {
		t.Fatalf("transfer row: %v", mv)
	}
	if p := byTitle["'@sum"]; p == nil || p[col("planned")] != "true" || p[col("date")] != "2026-12-01T00:00:00Z" {
		t.Fatalf("planned row: %v", p)
	}
	// Numeric cells are never prefixed.
	for _, r := range rows[1:] {
		for _, c := range []string{"amount_minor", "amount"} {
			if strings.HasPrefix(r[col(c)], "'") {
				t.Fatalf("numeric cell prefixed: %v", r)
			}
		}
	}
	// Newest first: planned (Dec) before June 3, 2, 1, then the adjustment rows.
	if rows[1][col("title")] != "'@sum" {
		t.Fatalf("order: %v", rows[1])
	}

	acc := e.exportCSV(alice, "/api/v1/me/export/accounts.csv")
	if len(acc) != 3 || acc[0][1] != "name" {
		t.Fatalf("accounts.csv: %v", acc)
	}
	found := false
	for _, r := range acc[1:] {
		if r[1] == "'=cmd|' /C calc'!A0" {
			found = true
		}
		if strings.Contains(strings.Join(r, "|"), "Mal") {
			t.Fatalf("foreign account: %v", r)
		}
	}
	if !found {
		t.Fatalf("accounts.csv: %v", acc)
	}
	bud := e.exportCSV(alice, "/api/v1/me/export/budgets.csv")
	if len(bud) != 2 || bud[1][1] != "Pets budget" || bud[1][2] != "5000" || bud[1][3] != "50.00" || bud[1][5] != "Pets" || bud[1][6] != "'=cmd|' /C calc'!A0" {
		t.Fatalf("budgets.csv: %v", bud)
	}
	loans := e.exportCSV(alice, "/api/v1/me/export/loans.csv")
	if len(loans) != 2 || loans[1][1] != "'=SUM(A1)" || loans[1][2] != "LEND" || loans[1][3] != "1000" || loans[1][11] != "10.00" || loans[1][12] != "false" {
		t.Fatalf("loans.csv: %v", loans)
	}

	// Mallory sees only hers; unauthenticated is refused; the legacy CSV is unchanged.
	mrows := e.exportCSV(mallory, "/api/v1/me/export/transactions.csv")
	for _, r := range mrows[1:] {
		if strings.Contains(strings.Join(r, "|"), "pizza") {
			t.Fatalf("alice data in mallory CSV: %v", r)
		}
	}
	if len(e.exportCSV(mallory, "/api/v1/me/export/loans.csv")) != 2 || len(e.exportCSV(mallory, "/api/v1/me/export/budgets.csv")) != 2 {
		t.Fatal("mallory loans/budgets")
	}
	for _, p := range []string{"transactions", "accounts", "budgets", "loans"} {
		if s, _, _ := e.raw("GET", "/api/v1/me/export/"+p+".csv", "", nil); s != 401 {
			t.Fatalf("unauthenticated %s: %d", p, s)
		}
	}
	status, _, raw := e.raw("GET", "/api/v1/me/export.csv", alice.token, nil)
	if status != 200 || !strings.HasPrefix(string(raw), "type,date,group,description,category,paid_by,amount,currency,split_user,split_amount,split_mode\n") {
		t.Fatalf("legacy export.csv changed: %d %q", status, raw)
	}
}
