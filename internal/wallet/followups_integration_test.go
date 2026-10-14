package wallet_test

import (
	"context"
	"fmt"
	"net/url"
	"testing"
	"time"

	"github.com/settlr-org/settlr-api/internal/testutil"
)

func TestIntegration_SummaryPaging(t *testing.T) {
	e := newEnv(t, testutil.CleanDB(t))
	alice := e.register("Alice", "alice-wallet-page@test.local")
	food := e.systemCategory("Food")
	bank := str(e.createAccount(alice, "Bank", "NPR", 0)["id"])
	for _, day := range []string{"2026-03-01", "2026-03-02", "2026-03-03"} {
		e.must(201, "POST", "/api/v1/transactions", alice, map[string]any{
			"type": "EXPENSE", "account_id": bank, "amount": 100, "category_id": food, "title": day, "occurred_at": day,
		})
	}
	for _, base := range []string{"/api/v1/accounts/" + bank, "/api/v1/categories/" + food} {
		path := base + "/summary?from=2026-03-01&to=2026-04-01&limit=2"
		first := e.must(200, "GET", path, alice, nil)
		cur := str(first["next_cursor"])
		if got := list(first, "entries"); len(got) != 2 || str(got[0]["title"]) != "2026-03-03" || cur == "" {
			t.Fatalf("%s first page: %v", base, first)
		}
		second := e.must(200, "GET", path+"&cursor="+url.QueryEscape(cur), alice, nil)
		if got := list(second, "entries"); len(got) != 1 || str(got[0]["title"]) != "2026-03-01" || second["next_cursor"] != nil {
			t.Fatalf("%s second page: %v", base, second)
		}
		// Totals cover the whole range, not just the page.
		if num(second["expense"]) != 300 {
			t.Fatalf("%s totals: %v", base, second)
		}
		e.status(422, "GET", path+"&cursor=bogus", alice, nil)
	}
}

func TestIntegration_CreateCategoryShape(t *testing.T) {
	e := newEnv(t, testutil.CleanDB(t))
	alice := e.register("Alice", "alice-wallet-cat@test.local")
	c := e.must(201, "POST", "/api/v1/categories", alice, map[string]any{"name": "Pets", "grouping": "Home"})
	if c["is_system"] != false || str(c["grouping"]) != "Home" || c["sort_order"] != nil || str(c["icon"]) != "tag" {
		t.Fatalf("created category: %v", c)
	}
	for _, x := range list(e.must(200, "GET", "/api/v1/categories", alice, nil), "data") {
		if str(x["id"]) == str(c["id"]) {
			if _, ok := x["sort_order"]; !ok || x["is_system"] != false || str(x["grouping"]) != "Home" {
				t.Fatalf("listed category: %v", x)
			}
			return
		}
	}
	t.Fatal("created category not listed")
}

func TestIntegration_CrossRatesInBalances(t *testing.T) {
	e := newEnv(t, testutil.CleanDB(t))
	alice := e.register("Alice", "alice-wallet-cross@test.local")
	// Default currency is NPR; the fake provider has no EUR/NPR quote either way,
	// so EUR converts via USD (EUR→USD inverse of USD→EUR, then USD→NPR).
	eur := e.createAccount(alice, "Euro", "EUR", 900)
	if b := num(eur["balance_base"]); b != 133000 {
		t.Fatalf("EUR via USD pivot: %v", eur)
	}
	// A user override for NPR/EUR is inverted and beats the cross rate.
	e.must(200, "PUT", "/api/v1/me/rates/NPR/EUR", alice, map[string]any{"rate": 0.008})
	if b := num(e.accountsByName(alice)["Euro"]["balance_base"]); b != 112500 {
		t.Fatalf("inverted override: %d", b)
	}
	ov := e.must(200, "GET", "/api/v1/overview", alice, nil)
	if len(ov["missing_rates"].([]any)) != 0 {
		t.Fatalf("no rates should be missing: %v", ov)
	}
	// JPY (zero decimals) rounds to whole yen when converted into.
	e.must(200, "PUT", "/api/v1/me/rates/USD/JPY", alice, map[string]any{"rate": 150})
	e.createAccount(alice, "Dollars", "USD", 333)
	if _, err := e.pool.Exec(context.Background(), `UPDATE users SET default_currency = 'JPY' WHERE id = $1`, alice.id); err != nil {
		t.Fatal(err)
	}
	// $3.33 → ¥499.5 → ¥500; €9 → $10 → ¥1500 (EUR→USD→JPY, override leg).
	ov = e.must(200, "GET", "/api/v1/overview", alice, nil)
	if str(ov["currency"]) != "JPY" || num(ov["balance"]) != 200000 {
		t.Fatalf("JPY overview: %v", ov)
	}
}

func TestIntegration_AdjustmentsAreNotFlows(t *testing.T) {
	e := newEnv(t, testutil.CleanDB(t))
	alice := e.register("Alice", "alice-wallet-adj@test.local")
	food := e.systemCategory("Food")
	// A starting balance of NPR 10,000 is not income.
	bank := str(e.createAccount(alice, "Bank", "NPR", 1000000)["id"])
	e.must(201, "POST", "/api/v1/transactions", alice, map[string]any{"type": "EXPENSE", "account_id": bank, "amount": 500, "category_id": food, "title": "Lunch"})
	e.must(201, "POST", "/api/v1/transactions", alice, map[string]any{"type": "INCOME", "account_id": bank, "amount": 2000, "category_id": food, "title": "Refund"})
	// A downward correction is not an expense either.
	e.must(200, "POST", "/api/v1/accounts/"+bank+"/adjust", alice, map[string]any{"balance": 900000})

	ov := e.must(200, "GET", "/api/v1/overview?from=2000-01-01&to=2100-01-01", alice, nil)
	if num(ov["income"]) != 2000 || num(ov["expense"]) != 500 || num(ov["balance"]) != 900000 {
		t.Fatalf("overview: %v", ov)
	}
	sum := e.must(200, "GET", "/api/v1/accounts/"+bank+"/summary?from=2000-01-01&to=2100-01-01", alice, nil)
	if num(sum["income"]) != 2000 || num(sum["expense"]) != 500 || num(sum["balance"]) != 900000 {
		t.Fatalf("account summary: %v", sum)
	}
	// Adjustments still move the account and appear in its entries.
	adjustments := 0
	for _, x := range list(sum, "entries") {
		if str(x["source"]) == "ADJUSTMENT" {
			adjustments++
		}
	}
	if num(sum["inflow"]) != 1000000+2000 || adjustments != 2 {
		t.Fatalf("account movements: %v", sum)
	}
	cs := e.must(200, "GET", "/api/v1/categories/"+food+"/summary?from=2000-01-01&to=2100-01-01", alice, nil)
	if num(cs["income"]) != 2000 || num(cs["expense"]) != 500 {
		t.Fatalf("category summary: %v", cs)
	}
}

func TestIntegration_LedgerSameDayOrder(t *testing.T) {
	e := newEnv(t, testutil.CleanDB(t))
	alice := e.register("Alice", "alice-wallet-order@test.local")
	g := e.must(201, "POST", "/api/v1/groups", alice, map[string]any{"name": "Trip", "currency": "NPR"})
	gid := str(g["id"])
	now := time.Now().UTC()
	today := now.Format("2006-01-02")
	yesterday := now.AddDate(0, 0, -1).Format("2006-01-02")
	bank := str(e.createAccount(alice, "Bank", "NPR", 0)["id"])

	addExpense := func(desc, date string) {
		e.must(201, "POST", "/api/v1/groups/"+gid+"/expenses", alice, map[string]any{
			"description": desc, "amount": 1000, "paid_by": alice.id.String(), "split_mode": "EQUAL",
			"splits": []map[string]string{{"user_id": alice.id.String()}}, "expense_date": date,
		})
	}
	// Backdated group expense: sorts at the start of its day.
	addExpense("Old dinner", yesterday)
	// Wallet transaction earlier today, then a group expense added later today.
	e.must(201, "POST", "/api/v1/transactions", alice, map[string]any{
		"type": "EXPENSE", "account_id": bank, "amount": 300, "title": "Coffee",
		"occurred_at": now.Add(-time.Second).Format(time.RFC3339Nano),
	})
	addExpense("Taxi", today)

	titles := func(rows []map[string]any) []string {
		out := []string{}
		for _, r := range rows {
			out = append(out, str(r["title"]))
		}
		return out
	}
	want := []string{"Taxi", "Coffee", "Old dinner"}
	if got := titles(list(e.must(200, "GET", "/api/v1/ledger", alice, nil), "data")); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("ledger order: %v, want %v", got, want)
	}
	// Cursor pagination walks the same order one entry at a time.
	var paged []string
	path := "/api/v1/ledger?limit=1"
	for range 5 {
		page := e.must(200, "GET", path, alice, nil)
		paged = append(paged, titles(list(page, "data"))...)
		cur := str(page["next_cursor"])
		if cur == "" {
			break
		}
		path = "/api/v1/ledger?limit=1&cursor=" + url.QueryEscape(cur)
	}
	if fmt.Sprint(paged) != fmt.Sprint(want) {
		t.Fatalf("paged ledger: %v, want %v", paged, want)
	}
	// Date filters still select by date.
	if got := titles(list(e.must(200, "GET", "/api/v1/ledger?from="+today+"&to="+today, alice, nil), "data")); fmt.Sprint(got) != "[Taxi Coffee]" {
		t.Fatalf("today filter: %v", got)
	}
	if got := titles(list(e.must(200, "GET", "/api/v1/ledger?from="+yesterday+"&to="+yesterday, alice, nil), "data")); fmt.Sprint(got) != "[Old dinner]" {
		t.Fatalf("yesterday filter: %v", got)
	}
}
