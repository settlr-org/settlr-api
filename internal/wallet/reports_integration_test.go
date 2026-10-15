package wallet_test

import (
	"context"
	"encoding/csv"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/settlr-org/settlr-api/internal/testutil"
)

// reportFixture is Alice's May 2026 (base USD; rates: 1 USD = 133 NPR = 0.9 EUR):
//
//	income   Salary                       100000 USD  (no category)
//	expense  Lunch pizza   Food  [tag t1]  10000 USD
//	expense  Rent share    Rent (custom)    5000 USD
//	expense  Dinner Paris  Food [holiday]   9000 EUR  = 10000 USD
//	expense  Momo          Food           133000 NPR =  1000 USD
//	group    Flat #1 (Alice paid 13300 NPR from Nepal, share 6650)   =  50 USD, Food, account Nepal
//	group    Flat #2 (Bob paid 26600 NPR, Alice share 13300)         = 100 USD, Food, no account
//	group    Flat #3 (Alice paid 4000 NPR for Bob only, from Nepal)  = share 0 (balance effect only)
//	transfer Bank -> Euro 9000 USD -> 8100 EUR (= 9000 USD)
//	not counted: initial balance (ADJUSTMENT), loan booking (LOAN), planned 2222 USD due May 20.
type reportFixture struct {
	e                       *env
	alice, bob              user
	bank, euro, nepal       string
	food, rent, t1, holiday string
}

func newReportFixture(t *testing.T) *reportFixture {
	e := newEnv(t, testutil.CleanDB(t))
	f := &reportFixture{e: e}
	f.alice = e.register("Alice", "alice-reports@test.local")
	f.bob = e.register("Bob", "bob-reports@test.local")
	e.makeFriends(f.alice.id, f.bob.id)
	e.useBase("USD", f.alice, f.bob)
	f.bank = str(e.createAccount(f.alice, "Bank", "USD", 50000)["id"])
	f.euro = str(e.createAccount(f.alice, "Euro", "EUR", 0)["id"])
	f.nepal = str(e.createAccount(f.alice, "Nepal", "NPR", 0)["id"])
	f.food = e.systemCategory("Food")
	f.rent = str(e.must(201, "POST", "/api/v1/categories", f.alice, map[string]any{"name": "Rent"})["id"])
	f.t1 = str(e.must(201, "POST", "/api/v1/tags", f.alice, map[string]any{"name": "t1", "color": "#111111"})["id"])
	f.holiday = str(e.must(201, "POST", "/api/v1/tags", f.alice, map[string]any{"name": "Holiday", "color": "#222222"})["id"])
	tx := func(body map[string]any) {
		e.must(201, "POST", "/api/v1/transactions", f.alice, body)
	}
	tx(map[string]any{"type": "INCOME", "account_id": f.bank, "amount": 100000, "title": "Salary", "occurred_at": "2026-05-05"})
	tx(map[string]any{"type": "EXPENSE", "account_id": f.bank, "amount": 10000, "title": "Lunch pizza", "category_id": f.food, "tag_ids": []string{f.t1}, "occurred_at": "2026-05-03"})
	tx(map[string]any{"type": "EXPENSE", "account_id": f.bank, "amount": 5000, "title": "Rent share", "description": "May flat", "category_id": f.rent, "occurred_at": "2026-05-04"})
	tx(map[string]any{"type": "EXPENSE", "account_id": f.euro, "amount": 9000, "title": "Dinner Paris", "category_id": f.food, "tag_ids": []string{f.holiday}, "occurred_at": "2026-05-06"})
	tx(map[string]any{"type": "EXPENSE", "account_id": f.nepal, "amount": 133000, "title": "Momo", "category_id": f.food, "occurred_at": "2026-05-06T12:00:00Z"})
	tx(map[string]any{"type": "TRANSFER", "account_id": f.bank, "to_account_id": f.euro, "amount": 9000, "to_amount": 8100, "title": "Top up euro", "occurred_at": "2026-05-07"})
	tx(map[string]any{"type": "EXPENSE", "account_id": f.bank, "amount": 2222, "title": "Planned gym", "due_at": "2026-05-20"})
	e.must(201, "POST", "/api/v1/loans", f.alice, map[string]any{"name": "Loan to Bob", "type": "LEND", "amount": 3000, "account_id": f.bank, "occurred_at": "2026-05-08"})

	g := e.must(201, "POST", "/api/v1/groups", f.alice, map[string]any{"name": "Flat", "currency": "NPR"})
	gid := str(g["id"])
	e.must(201, "POST", "/api/v1/groups/"+gid+"/members", f.alice, map[string]any{"user_id": f.bob.id.String()})
	exp := func(u, paidBy user, amount int64, with []user, extra map[string]any) {
		splits := []map[string]string{}
		for _, s := range with {
			splits = append(splits, map[string]string{"user_id": s.id.String()})
		}
		body := map[string]any{"description": "Groceries", "amount": amount, "paid_by": paidBy.id.String(), "split_mode": "EQUAL", "splits": splits, "category_id": f.food, "expense_date": "2026-05-10"}
		for k, v := range extra {
			body[k] = v
		}
		e.must(201, "POST", "/api/v1/groups/"+gid+"/expenses", u, body)
	}
	exp(f.alice, f.alice, 13300, []user{f.alice, f.bob}, map[string]any{"payer_account_id": f.nepal})
	exp(f.bob, f.bob, 26600, []user{f.alice, f.bob}, map[string]any{"description": "Rice"})
	exp(f.alice, f.alice, 4000, []user{f.bob}, map[string]any{"payer_account_id": f.nepal, "description": "Fronted"})
	return f
}

func (f *reportFixture) report(u user, q string) map[string]any {
	f.e.t.Helper()
	return f.e.must(200, "GET", "/api/v1/reports?"+q, u, nil)
}

const may = "from=2026-05-01&to=2026-05-31"

func checkTotals(t *testing.T, label string, r map[string]any, income, expense int64, count int64) {
	t.Helper()
	if num(r["income"]) != income || num(r["expense"]) != expense || num(r["net"]) != income-expense || num(r["count"]) != count {
		t.Fatalf("%s: income=%v expense=%v net=%v count=%v, want %d/%d/%d/%d", label, r["income"], r["expense"], r["net"], r["count"], income, expense, income-expense, count)
	}
}

func TestIntegration_ReportTotalsAndBreakdowns(t *testing.T) {
	f := newReportFixture(t)
	e := f.e

	r := f.report(f.alice, may)
	if str(r["currency"]) != "USD" || str(r["breakdown"]) != "expense" {
		t.Fatalf("header: %v", r)
	}
	// Food 10000+10000+1000+50+100, Rent 5000.
	checkTotals(t, "may", r, 100000, 26150, 7)
	if num(r["transfers_in"]) != 9000 || num(r["transfers_out"]) != 9000 {
		t.Fatalf("transfers: %v %v", r["transfers_in"], r["transfers_out"])
	}
	if ms := r["missing_rates"].([]any); len(ms) != 0 {
		t.Fatalf("missing: %v", ms)
	}
	cats := list(r, "by_category")
	if len(cats) != 2 || str(cats[0]["category_id"]) != f.food || str(cats[0]["name"]) != "Food" || num(cats[0]["amount"]) != 21150 || num(cats[0]["count"]) != 5 ||
		str(cats[1]["category_id"]) != f.rent || str(cats[1]["name"]) != "Rent" || num(cats[1]["amount"]) != 5000 || num(cats[1]["count"]) != 1 {
		t.Fatalf("by_category: %v", cats)
	}
	// 21150/26150 = 80.88 %, 5000/26150 = 19.12 %: largest remainder over tenths -> 80.9 + 19.1.
	if cats[0]["percent"].(float64) != 80.9 || cats[1]["percent"].(float64) != 19.1 {
		t.Fatalf("percent: %v %v", cats[0]["percent"], cats[1]["percent"])
	}
	for _, k := range []string{"icon", "color"} {
		if str(cats[0][k]) == "" {
			t.Fatalf("category %s missing: %v", k, cats[0])
		}
	}

	// Per account: own currency amounts plus base amounts; "No account" for group shares someone else paid.
	accs := map[string]map[string]any{}
	for _, a := range list(r, "by_account") {
		accs[str(a["name"])] = a
	}
	if len(accs) != 4 {
		t.Fatalf("by_account: %v", r["by_account"])
	}
	if a := accs["Bank"]; str(a["currency"]) != "USD" || num(a["income"]) != 100000 || num(a["expense"]) != 15000 || num(a["net"]) != 85000 || str(a["account_id"]) != f.bank {
		t.Fatalf("Bank: %v", a)
	}
	if a := accs["Euro"]; str(a["currency"]) != "EUR" || num(a["expense"]) != 9000 || num(a["base_expense"]) != 10000 || num(a["net"]) != -9000 || num(a["base_net"]) != -10000 {
		t.Fatalf("Euro: %v", a)
	}
	if a := accs["Nepal"]; str(a["currency"]) != "NPR" || num(a["expense"]) != 133000+6650 || num(a["base_expense"]) != 1050 {
		t.Fatalf("Nepal: %v", a)
	}
	if a := accs["No account"]; a["account_id"] != nil || str(a["currency"]) != "USD" || num(a["expense"]) != 100 || num(a["base_expense"]) != 100 {
		t.Fatalf("No account: %v", a)
	}

	// All time equals May: adjustments, loan bookings and planned items never count.
	checkTotals(t, "all time", f.report(f.alice, ""), 100000, 26150, 7)
	if r := f.report(f.alice, "source=LOAN"); num(r["expense"]) != 0 || num(r["income"]) != 0 {
		t.Fatalf("loan source: %v", r)
	}
	if r := f.report(f.alice, "source=ADJUSTMENT"); num(r["income"]) != 0 || num(r["count"]) != 0 {
		t.Fatalf("adjustment source: %v", r)
	}
	if r := f.report(f.alice, "hide_adjustments=true&"+may); r["hide_adjustments"] != true {
		t.Fatalf("hide_adjustments echo: %v", r)
	}

	// breakdown=income
	r = f.report(f.alice, may+"&breakdown=income")
	cats = list(r, "by_category")
	if len(cats) != 1 || cats[0]["category_id"] != nil || str(cats[0]["name"]) != "Uncategorized" || num(cats[0]["amount"]) != 100000 || cats[0]["percent"].(float64) != 100 {
		t.Fatalf("income breakdown: %v", cats)
	}

	// Planned: opt in with planned=all|true; they carry the due date.
	r = f.report(f.alice, may+"&planned=all")
	checkTotals(t, "planned=all", r, 100000, 26150+2222, 8)
	r = f.report(f.alice, may+"&planned=true")
	checkTotals(t, "planned=true", r, 0, 2222, 1)

	// Group shares can be left out.
	r = f.report(f.alice, may+"&include_group_shares=false")
	checkTotals(t, "no group shares", r, 100000, 26000, 5)
	if cats := list(r, "by_category"); num(cats[0]["amount"]) != 21000 {
		t.Fatalf("no group shares category: %v", cats)
	}
	for _, a := range list(r, "by_account") {
		if str(a["name"]) == "No account" {
			t.Fatalf("No account row without group shares: %v", a)
		}
	}
	checkTotals(t, "exclude_source=GROUP", f.report(f.alice, may+"&exclude_source=GROUP"), 100000, 26000, 5)
	checkTotals(t, "source=GROUP", f.report(f.alice, may+"&source=GROUP"), 0, 150, 2)

	// Filters.
	checkTotals(t, "type=EXPENSE", f.report(f.alice, may+"&type=EXPENSE"), 0, 26150, 6)
	checkTotals(t, "type=INCOME", f.report(f.alice, may+"&type=INCOME"), 100000, 0, 1)
	r = f.report(f.alice, may+"&type=TRANSFER")
	checkTotals(t, "type=TRANSFER", r, 0, 0, 0)
	if num(r["transfers_in"]) != 9000 {
		t.Fatalf("transfer-only: %v", r)
	}
	checkTotals(t, "category=Rent", f.report(f.alice, may+"&category="+f.rent), 0, 5000, 1)
	checkTotals(t, "category=Food,Rent", f.report(f.alice, may+"&category="+f.food+","+f.rent), 0, 26150, 6)
	r = f.report(f.alice, may+"&category=none")
	checkTotals(t, "category=none", r, 100000, 0, 1)
	checkTotals(t, "category=none,Rent", f.report(f.alice, may+"&category=none,"+f.rent), 100000, 5000, 2)
	checkTotals(t, "tag=t1", f.report(f.alice, may+"&tag="+f.t1), 0, 10000, 1)
	checkTotals(t, "tag=t1,holiday", f.report(f.alice, may+"&tag="+f.t1+"&tag="+f.holiday), 0, 20000, 2)
	checkTotals(t, "excl_tag=t1", f.report(f.alice, may+"&excl_tag="+f.t1), 100000, 16150, 6)
	checkTotals(t, "account=Euro", f.report(f.alice, may+"&account="+f.euro), 0, 10000, 1)
	r = f.report(f.alice, may+"&account="+f.euro)
	if num(r["transfers_in"]) != 9000 || num(r["transfers_out"]) != 0 {
		t.Fatalf("account=Euro transfers: %v", r)
	}
	r = f.report(f.alice, may+"&account="+f.bank)
	checkTotals(t, "account=Bank", r, 100000, 15000, 3)
	if num(r["transfers_in"]) != 0 || num(r["transfers_out"]) != 9000 {
		t.Fatalf("account=Bank transfers: %v", r)
	}
	checkTotals(t, "account=Bank,Euro", f.report(f.alice, may+"&account="+f.bank+","+f.euro), 100000, 25000, 4)
	// Keywords: ALL include terms, ANY exclude term; title, description and tag names, case-insensitive.
	checkTotals(t, "q=pizza", f.report(f.alice, may+"&q=PIZZA"), 0, 10000, 1)
	checkTotals(t, "q=lunch pizza", f.report(f.alice, may+"&q=lunch+pizza"), 0, 10000, 1)
	checkTotals(t, "q=lunch paris", f.report(f.alice, may+"&q=lunch+paris"), 0, 0, 0)
	checkTotals(t, "q=flat (description)", f.report(f.alice, may+"&q=flat"), 0, 5000, 1)
	checkTotals(t, "q=holiday (tag)", f.report(f.alice, may+"&q=holiday"), 0, 10000, 1)
	checkTotals(t, "q=holiday paris", f.report(f.alice, may+"&q=holiday+paris"), 0, 10000, 1)
	checkTotals(t, "q=groceries (group)", f.report(f.alice, may+"&q=groceries"), 0, 50, 1)
	checkTotals(t, "excl_q=pizza", f.report(f.alice, may+"&excl_q=pizza"), 100000, 16150, 6)
	checkTotals(t, "excl_q=pizza paris", f.report(f.alice, may+"&excl_q=pizza+paris"), 100000, 6150, 5)
	checkTotals(t, "excl_q=holiday (tag)", f.report(f.alice, may+"&excl_q=holiday"), 100000, 16150, 6)
	checkTotals(t, "q=rent excl_q=flat", f.report(f.alice, may+"&q=rent&excl_q=flat"), 0, 0, 0)
	// min/max apply to the entry's own amount (minor units of its own currency).
	checkTotals(t, "min=10000", f.report(f.alice, may+"&min=10000"), 100000, 11100, 4)
	checkTotals(t, "max=5000", f.report(f.alice, may+"&max=5000"), 0, 5000, 1)
	checkTotals(t, "min=5000&max=9000", f.report(f.alice, may+"&min=5000&max=9000"), 0, 5000+10000+50, 3)
	// Range edges: [from, to) with date-only `to` inclusive of that day.
	checkTotals(t, "May 3 only", f.report(f.alice, "from=2026-05-03&to=2026-05-03"), 0, 10000, 1)
	checkTotals(t, "April", f.report(f.alice, "from=2026-04-01&to=2026-04-30"), 0, 0, 0)

	// Transfers as flow: income/expense include the legs, per currency pair.
	e.must(200, "PATCH", "/api/v1/me/settings", f.alice, map[string]any{"treat_transfers_as_flow": true})
	r = f.report(f.alice, may)
	checkTotals(t, "flow on", r, 109000, 35150, 8)
	if r["treat_transfers_as_flow"] != true || num(r["transfers_in"]) != 9000 || num(r["transfers_out"]) != 9000 {
		t.Fatalf("flow on: %v", r)
	}
	for _, a := range list(r, "by_account") {
		switch str(a["name"]) {
		case "Euro":
			if num(a["income"]) != 8100 || num(a["expense"]) != 9000 {
				t.Fatalf("Euro flow: %v", a)
			}
		case "Bank":
			if num(a["income"]) != 100000 || num(a["expense"]) != 15000+9000 {
				t.Fatalf("Bank flow: %v", a)
			}
		}
	}
	e.must(200, "PATCH", "/api/v1/me/settings", f.alice, map[string]any{"treat_transfers_as_flow": false})
	checkTotals(t, "flow off again", f.report(f.alice, may), 100000, 26150, 7)

	// Rate overrides win over provider rates; unknown pairs are reported, not guessed.
	e.must(200, "PUT", "/api/v1/me/rates/EUR/USD", f.alice, map[string]any{"rate": 2})
	checkTotals(t, "override", f.report(f.alice, may+"&account="+f.euro), 0, 18000, 1)
	e.must(200, "DELETE", "/api/v1/me/rates/EUR/USD", f.alice, nil)
	if _, err := e.pool.Exec(context.Background(), `UPDATE users SET default_currency = 'JPY' WHERE id = $1`, f.alice.id); err != nil {
		t.Fatal(err)
	}
	r = f.report(f.alice, may)
	missing := fmt.Sprint(r["missing_rates"])
	if !strings.Contains(missing, "USD/JPY") {
		t.Fatalf("missing_rates: %v", r["missing_rates"])
	}
	if num(r["income"]) != 0 {
		t.Fatalf("unconvertible amounts must not be summed: %v", r)
	}

	// Isolation: Bob sees only his own shares, and Alice's ids match nothing for him.
	rb := f.report(f.bob, may)
	if num(rb["income"]) != 0 || num(rb["expense"]) != 180 {
		t.Fatalf("bob: %v", rb)
	}
	if blob := fmt.Sprint(rb); strings.Contains(blob, "Salary") || strings.Contains(blob, f.bank) || strings.Contains(blob, "Rent") {
		t.Fatalf("leak to bob: %v", rb)
	}
	r = f.report(f.bob, may+"&account="+f.bank)
	checkTotals(t, "bob with alice's account", r, 0, 0, 0)
	r = f.report(f.bob, may+"&category="+f.rent)
	checkTotals(t, "bob with alice's category", r, 0, 0, 0)
}

func TestIntegration_ReportBobGroupShares(t *testing.T) {
	f := newReportFixture(t)
	// Bob: #1 share 6650 NPR (50), #2 paid 26600 share 13300 (100), #3 share 4000 NPR (30.07 -> 30).
	r := f.report(f.bob, may)
	if num(r["income"]) != 0 || num(r["expense"]) != 50+100+30 || num(r["count"]) != 3 {
		t.Fatalf("bob totals: %v", r)
	}
	accs := list(r, "by_account")
	if len(accs) != 1 || accs[0]["account_id"] != nil || str(accs[0]["name"]) != "No account" {
		t.Fatalf("bob has no accounts: %v", accs)
	}
	// Bob paid #2 without naming an account: still only his share counts.
	if cats := list(r, "by_category"); len(cats) != 1 || num(cats[0]["amount"]) != 180 || num(cats[0]["count"]) != 3 {
		t.Fatalf("bob categories: %v", cats)
	}
}

func TestIntegration_ReportExcludedAccountsAndSettlements(t *testing.T) {
	e := newEnv(t, testutil.CleanDB(t))
	alice := e.register("Alice", "alice-rep-vault@test.local")
	bob := e.register("Bob", "bob-rep-vault@test.local")
	e.makeFriends(alice.id, bob.id)
	e.useBase("USD", alice, bob)
	main := str(e.createAccount(alice, "Main", "USD", 0)["id"])
	vault := str(e.createAccount(alice, "Vault", "USD", 0)["id"])
	e.must(200, "PATCH", "/api/v1/accounts/"+vault, alice, map[string]any{"include_in_balance": false})
	e.must(201, "POST", "/api/v1/transactions", alice, map[string]any{"type": "EXPENSE", "account_id": main, "amount": 1000, "occurred_at": "2026-05-02"})
	e.must(201, "POST", "/api/v1/transactions", alice, map[string]any{"type": "EXPENSE", "account_id": vault, "amount": 7777, "occurred_at": "2026-05-02"})
	// Same rule as /overview by default; an explicit account filter overrides it.
	r := e.must(200, "GET", "/api/v1/reports?"+may, alice, nil)
	checkTotals(t, "default", r, 0, 1000, 1)
	ov := e.must(200, "GET", "/api/v1/overview?"+may, alice, nil)
	if num(ov["expense"]) != num(r["expense"]) {
		t.Fatalf("overview %v vs report %v", ov["expense"], r["expense"])
	}
	checkTotals(t, "vault filter", e.must(200, "GET", "/api/v1/reports?"+may+"&account="+vault, alice, nil), 0, 7777, 1)

	// Settlements never count.
	g := e.must(201, "POST", "/api/v1/groups", alice, map[string]any{"name": "Trip", "currency": "USD"})
	gid := str(g["id"])
	e.must(201, "POST", "/api/v1/groups/"+gid+"/members", alice, map[string]any{"user_id": bob.id.String()})
	bw := str(e.createAccount(bob, "Wallet", "USD", 0)["id"])
	e.must(201, "POST", "/api/v1/groups/"+gid+"/settlements", bob, map[string]any{"from_user": bob.id.String(), "to_user": alice.id.String(), "amount": 2500, "from_account_id": bw})
	rb := e.must(200, "GET", "/api/v1/reports", bob, nil)
	checkTotals(t, "settlement", rb, 0, 0, 0)
	if len(list(rb, "by_category")) != 0 || len(list(rb, "by_account")) != 0 {
		t.Fatalf("settlement must not appear: %v", rb)
	}
	if got := e.must(200, "GET", "/api/v1/reports?type=SETTLEMENT", bob, nil); num(got["count"]) != 0 {
		t.Fatalf("type=SETTLEMENT: %v", got)
	}
}

func TestIntegration_ReportKeywordEscaping(t *testing.T) {
	e := newEnv(t, testutil.CleanDB(t))
	alice := e.register("Alice", "alice-rep-q@test.local")
	e.useBase("USD", alice)
	acc := str(e.createAccount(alice, "Main", "USD", 0)["id"])
	add := func(title string, amount int64) {
		e.must(201, "POST", "/api/v1/transactions", alice, map[string]any{"type": "EXPENSE", "account_id": acc, "amount": amount, "title": title, "occurred_at": "2026-05-02"})
	}
	add("50% off coupon", 100)
	add("under_score", 200)
	add(`back\slash`, 400)
	add("plain", 800)
	count := func(q string) int64 {
		return num(e.must(200, "GET", "/api/v1/reports?q="+url.QueryEscape(q), alice, nil)["count"])
	}
	for q, want := range map[string]int64{"%": 1, "_": 1, `\`: 1, "50%": 1, "r_s": 1, "%%": 0, "p%n": 0, "pl_in": 0, "plain": 1} {
		if got := count(q); got != want {
			t.Fatalf("q=%q count %d want %d", q, got, want)
		}
	}
	// Same behaviour on /ledger and /transactions (shared filters).
	for _, path := range []string{"/api/v1/ledger", "/api/v1/transactions"} {
		got := list(e.must(200, "GET", path+"?q="+url.QueryEscape("%")+"&exclude_source=ADJUSTMENT", alice, nil), "data")
		if len(got) != 1 {
			t.Fatalf("%s q=%%: %v", path, got)
		}
		got = list(e.must(200, "GET", path+"?excl_q="+url.QueryEscape("_")+"&exclude_source=ADJUSTMENT", alice, nil), "data")
		if len(got) != 3 {
			t.Fatalf("%s excl_q=_: %d", path, len(got))
		}
		got = list(e.must(200, "GET", path+"?category=none&min=300&exclude_source=ADJUSTMENT", alice, nil), "data")
		if len(got) != 2 {
			t.Fatalf("%s category=none&min=300: %d", path, len(got))
		}
	}
}

func TestIntegration_ReportValidationAndAuth(t *testing.T) {
	f := newReportFixture(t)
	e := f.e
	for _, p := range []string{"/api/v1/reports", "/api/v1/reports/trend?from=2026-05-01&to=2026-05-31", "/api/v1/reports/export.csv"} {
		if s, _ := e.do("GET", p, "", nil); s != 401 {
			t.Fatalf("%s without auth: %d", p, s)
		}
	}
	bad := []string{
		"/api/v1/reports?from=nope",
		"/api/v1/reports?from=2026-05-10&to=2026-05-01",
		"/api/v1/reports?from=2010-01-01&to=2026-05-01",
		"/api/v1/reports?breakdown=transfer",
		"/api/v1/reports?type=BOGUS",
		"/api/v1/reports?account=not-a-uuid",
		"/api/v1/reports?category=none,not-a-uuid",
		"/api/v1/reports?tag=" + strings.Repeat("00000000-0000-0000-0000-000000000001,", 101),
		"/api/v1/reports?min=abc",
		"/api/v1/reports?min=10&max=5",
		"/api/v1/reports?source=NOPE",
		"/api/v1/reports?hide_adjustments=maybe",
		"/api/v1/reports?include_group_shares=maybe",
		"/api/v1/reports/trend?bucket=day",
		"/api/v1/reports/trend?from=2026-05-01&bucket=day",
		"/api/v1/reports/trend?from=2026-05-01&to=2026-05-31&bucket=year",
		"/api/v1/reports/trend?from=2024-01-01&to=2026-05-31&bucket=day",
		"/api/v1/reports/trend?from=2000-01-01&to=2026-05-31&bucket=month",
		"/api/v1/reports/export.csv?type=BOGUS",
		"/api/v1/reports/export.csv?from=2026-05-10&to=2026-05-01",
	}
	for _, p := range bad {
		s, out := e.do("GET", p, f.alice.token, nil)
		errObj, _ := out["error"].(map[string]any)
		if s != 422 || str(errObj["code"]) == "" || str(errObj["message"]) == "" {
			t.Fatalf("%s: %d %v", strings.SplitN(p, "?", 2)[0]+"?...", s, out)
		}
	}
	// A limit of exactly 400 buckets is fine; 401 is not, and the message says what to do.
	e.must(200, "GET", "/api/v1/reports/trend?bucket=day&from=2026-01-01&to=2027-02-04", f.alice, nil)
	_, out := e.do("GET", "/api/v1/reports/trend?bucket=day&from=2026-01-01&to=2027-02-05", f.alice.token, nil)
	if msg := str(out["error"].(map[string]any)["message"]); !strings.Contains(msg, "400") {
		t.Fatalf("bucket cap message: %q", msg)
	}
}

func TestIntegration_ReportTrend(t *testing.T) {
	f := newReportFixture(t)
	e := f.e
	tr := func(q string) map[string]any { return e.must(200, "GET", "/api/v1/reports/trend?"+q, f.alice, nil) }

	// Daily, zero-filled: May 3..7 inclusive.
	r := tr("bucket=day&from=2026-05-03&to=2026-05-07")
	b := list(r, "buckets")
	if str(r["currency"]) != "USD" || str(r["bucket"]) != "day" || len(b) != 5 {
		t.Fatalf("day trend: %v", r)
	}
	wantExp := []int64{10000, 5000, 100000 * 0, 10000 + 1000, 0}
	wantInc := []int64{0, 0, 100000, 0, 0}
	for i, bk := range b {
		day := fmt.Sprintf("2026-05-%02dT00:00:00Z", 3+i)
		next := fmt.Sprintf("2026-05-%02dT00:00:00Z", 4+i)
		if str(bk["start"]) != day || str(bk["end"]) != next {
			t.Fatalf("bucket %d bounds: %v", i, bk)
		}
		if num(bk["expense"]) != wantExp[i] || num(bk["income"]) != wantInc[i] || num(bk["net"]) != wantInc[i]-wantExp[i] {
			t.Fatalf("bucket %d: %v want exp=%d inc=%d", i, bk, wantExp[i], wantInc[i])
		}
	}

	// Weeks start Monday: May 4 2026 is a Monday; May 3 (Sunday) belongs to the week of Apr 27.
	r = tr("bucket=week&from=2026-05-01&to=2026-05-31")
	b = list(r, "buckets")
	starts := []string{"2026-04-27", "2026-05-04", "2026-05-11", "2026-05-18", "2026-05-25"}
	if len(b) != len(starts) {
		t.Fatalf("week buckets: %v", b)
	}
	for i, bk := range b {
		if !strings.HasPrefix(str(bk["start"]), starts[i]) {
			t.Fatalf("week %d start %v", i, bk["start"])
		}
	}
	// Apr27 week: pizza (May 3). May 4 week: rent, salary, paris, momo, group entries (May 10).
	if num(b[0]["expense"]) != 10000 || num(b[1]["expense"]) != 5000+10000+1000+150 || num(b[1]["income"]) != 100000 || num(b[2]["expense"]) != 0 {
		t.Fatalf("week sums: %v", b)
	}

	// Months across a year boundary, including empty buckets; totals match /reports.
	r = tr("bucket=month&from=2025-12-15&to=2026-06-01")
	b = list(r, "buckets")
	if len(b) != 7 || !strings.HasPrefix(str(b[0]["start"]), "2025-12-01") || !strings.HasPrefix(str(b[0]["end"]), "2026-01-01") ||
		!strings.HasPrefix(str(b[1]["start"]), "2026-01-01") || !strings.HasPrefix(str(b[6]["start"]), "2026-06-01") {
		t.Fatalf("month buckets: %v", b)
	}
	var inc, exp int64
	for i, bk := range b {
		inc += num(bk["income"])
		exp += num(bk["expense"])
		if i != 5 && (num(bk["income"]) != 0 || num(bk["expense"]) != 0) {
			t.Fatalf("bucket %d should be empty: %v", i, bk)
		}
	}
	if inc != 100000 || exp != 26150 {
		t.Fatalf("month totals %d %d", inc, exp)
	}

	// Month end boundaries (UTC): an entry at 23:59:59 on Apr 30 and one at 00:00:00 on May 1.
	acc := f.bank
	e.must(201, "POST", "/api/v1/transactions", f.alice, map[string]any{"type": "EXPENSE", "account_id": acc, "amount": 7, "occurred_at": "2026-04-30T23:59:59Z"})
	e.must(201, "POST", "/api/v1/transactions", f.alice, map[string]any{"type": "EXPENSE", "account_id": acc, "amount": 11, "occurred_at": "2026-05-01T00:00:00Z"})
	e.must(201, "POST", "/api/v1/transactions", f.alice, map[string]any{"type": "EXPENSE", "account_id": acc, "amount": 13, "occurred_at": "2026-05-31T23:59:59Z"})
	b = list(tr("bucket=month&from=2026-04-01&to=2026-06-30"), "buckets")
	if len(b) != 3 || num(b[0]["expense"]) != 7 || num(b[1]["expense"]) != 26150+11+13 || num(b[2]["expense"]) != 0 {
		t.Fatalf("month end: %v", b)
	}
	// Same boundaries with a +05:45 offset in the request still bucket in UTC.
	b = list(tr("bucket=day&from="+url.QueryEscape("2026-05-01T00:00:00+05:45")+"&to="+url.QueryEscape("2026-05-02T00:00:00+05:45")), "buckets")
	if len(b) != 2 || !strings.HasPrefix(str(b[0]["start"]), "2026-04-30") {
		t.Fatalf("offset range: %v", b)
	}
	if num(b[0]["expense"]) != 7 { // 23:59:59Z Apr 30 is inside [Apr 30 18:15Z, May 1 18:15Z)
		t.Fatalf("offset range sums: %v", b)
	}

	// month_start_day = 15: buckets run 15th to 15th.
	e.must(200, "PATCH", "/api/v1/me/settings", f.alice, map[string]any{"month_start_day": 15})
	b = list(tr("bucket=month&from=2026-05-01&to=2026-06-01"), "buckets")
	if len(b) != 2 || !strings.HasPrefix(str(b[0]["start"]), "2026-04-15") || !strings.HasPrefix(str(b[0]["end"]), "2026-05-15") ||
		!strings.HasPrefix(str(b[1]["start"]), "2026-05-15") || !strings.HasPrefix(str(b[1]["end"]), "2026-06-15") {
		t.Fatalf("start day 15: %v", b)
	}
	// Apr15..May15 holds all of May up to the 14th (the Apr 30 entry is before `from`); May 31 is in the next bucket.
	if num(b[0]["expense"]) != 26150+11 || num(b[1]["expense"]) != 13 {
		t.Fatalf("start day 15 sums: %v", b)
	}

	// Filters apply to the trend as well.
	e.must(200, "PATCH", "/api/v1/me/settings", f.alice, map[string]any{"month_start_day": 1})
	b = list(tr("bucket=month&from=2026-05-01&to=2026-05-31&category="+f.rent), "buckets")
	if len(b) != 1 || num(b[0]["expense"]) != 5000 || num(b[0]["income"]) != 0 {
		t.Fatalf("filtered trend: %v", b)
	}
	// Isolation.
	b = list(e.must(200, "GET", "/api/v1/reports/trend?bucket=month&from=2026-05-01&to=2026-05-31", f.bob, nil), "buckets")
	if len(b) != 1 || num(b[0]["income"]) != 0 || num(b[0]["expense"]) != 180 {
		t.Fatalf("bob trend: %v", b)
	}
	// Default bucket is month.
	if r := tr("from=2026-05-01&to=2026-05-31"); str(r["bucket"]) != "month" {
		t.Fatalf("default bucket: %v", r["bucket"])
	}
}

func readCSV(t *testing.T, body []byte) [][]string {
	t.Helper()
	rows, err := csv.NewReader(strings.NewReader(string(body))).ReadAll()
	if err != nil {
		t.Fatalf("csv: %v\n%s", err, body)
	}
	return rows
}

func TestIntegration_ReportCSV(t *testing.T) {
	f := newReportFixture(t)
	e := f.e
	// Hostile cells.
	e.must(201, "POST", "/api/v1/transactions", f.alice, map[string]any{"type": "EXPENSE", "account_id": f.bank, "amount": 1, "title": `=HYPERLINK("http://x","y")`, "description": "line1\nline2, \"quoted\"", "category_id": f.rent, "occurred_at": "2026-05-12"})
	e.must(201, "POST", "/api/v1/transactions", f.alice, map[string]any{"type": "EXPENSE", "account_id": f.bank, "amount": 2, "title": "@cmd", "occurred_at": "2026-05-12T00:00:00Z"})

	resp := func(u user, q string) (int, http.Header, []byte) {
		req, _ := http.NewRequest("GET", e.srv.URL+"/api/v1/reports/export.csv?"+q, nil)
		req.Header.Set("Authorization", "Bearer "+u.token)
		r, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer r.Body.Close()
		var b strings.Builder
		buf := make([]byte, 4096)
		for {
			n, err := r.Body.Read(buf)
			b.Write(buf[:n])
			if err != nil {
				break
			}
		}
		return r.StatusCode, r.Header, []byte(b.String())
	}
	s, h, body := resp(f.alice, may+"&exclude_source=ADJUSTMENT,LOAN")
	if s != 200 || h.Get("Content-Type") != "text/csv; charset=utf-8" ||
		h.Get("Content-Disposition") != `attachment; filename="settlr-report-2026-05-01-2026-05-31.csv"` {
		t.Fatalf("headers: %d %v", s, h)
	}
	rows := readCSV(t, body)
	wantHeader := "date,kind,type,title,description,account,to_account,category,tags,amount_minor,amount,currency,to_amount_minor,to_amount,to_currency,base_amount,base_currency,group,source"
	if strings.Join(rows[0], ",") != wantHeader {
		t.Fatalf("header: %v", rows[0])
	}
	// 7 counted entries + transfer + payer-without-share group entry + 2 hostile rows (planned excluded).
	if len(rows)-1 != 11 {
		t.Fatalf("rows: %d\n%s", len(rows)-1, body)
	}
	// Newest first.
	dates := []string{}
	for _, r := range rows[1:] {
		dates = append(dates, r[0])
	}
	for i := 1; i < len(dates); i++ {
		if dates[i-1] < dates[i] {
			t.Fatalf("not newest first: %v", dates)
		}
	}
	by := map[string][]string{}
	for _, r := range rows[1:] {
		by[r[3]] = r
	}
	if r := by["'=HYPERLINK(\"http://x\",\"y\")"]; r == nil || r[4] != "line1\nline2, \"quoted\"" || r[7] != "Rent" || r[1] != "TRANSACTION" || r[2] != "EXPENSE" || r[5] != "Bank" {
		t.Fatalf("hostile row: %v", r)
	}
	if r := by["'@cmd"]; r == nil {
		t.Fatalf("@ not neutralised: %v", by)
	}
	if r := by["Dinner Paris"]; r == nil || r[5] != "Euro" || r[8] != "Holiday" || r[9] != "9000" || r[10] != "90.00" || r[11] != "EUR" || r[12] != "" || r[14] != "" || r[15] != "100.00" || r[16] != "USD" || r[17] != "" || r[18] != "MANUAL" {
		t.Fatalf("Paris row: %v", r)
	}
	if r := by["Top up euro"]; r == nil || r[2] != "TRANSFER" || r[5] != "Bank" || r[6] != "Euro" || r[9] != "9000" || r[12] != "8100" || r[13] != "81.00" || r[14] != "EUR" || r[15] != "90.00" {
		t.Fatalf("transfer row: %v", r)
	}
	groupRows := 0
	for _, r := range rows[1:] {
		if r[1] == "GROUP_EXPENSE" {
			groupRows++
			if r[17] != "Flat" || r[18] != "GROUP" || r[11] != "NPR" {
				t.Fatalf("group row: %v", r)
			}
		}
	}
	if groupRows != 3 {
		t.Fatalf("group rows: %d", groupRows)
	}
	if r := by["Groceries"]; r == nil || (r[9] != "6650" && r[9] != "13300") {
		t.Fatalf("group share amount: %v", by["Groceries"])
	}

	// Filters narrow the export exactly like /ledger.
	_, _, body = resp(f.alice, may+"&q=pizza")
	if rows := readCSV(t, body); len(rows) != 2 || rows[1][3] != "Lunch pizza" || rows[1][8] != "t1" {
		t.Fatalf("q=pizza: %v", rows)
	}
	_, _, body = resp(f.alice, may+"&include_group_shares=false&type=INCOME")
	if rows := readCSV(t, body); len(rows) != 2 || rows[1][3] != "Salary" {
		t.Fatalf("income only: %v", rows)
	}
	// Open-ended range filename; header only when nothing matches.
	s, h, body = resp(f.alice, "q=nothing-matches-this")
	if s != 200 || h.Get("Content-Disposition") != `attachment; filename="settlr-report-all-all.csv"` || len(readCSV(t, body)) != 1 {
		t.Fatalf("empty export: %d %v %s", s, h, body)
	}
	// Isolation.
	_, _, body = resp(f.bob, may)
	if strings.Contains(string(body), "Salary") || strings.Contains(string(body), "Paris") || strings.Contains(string(body), "Bank") {
		t.Fatalf("leak to bob:\n%s", body)
	}
	if rows := readCSV(t, body); len(rows)-1 != 3 {
		t.Fatalf("bob rows: %v", rows)
	}
	// Planned entries only on request.
	_, _, body = resp(f.alice, may+"&planned=true")
	if rows := readCSV(t, body); len(rows) != 2 || rows[1][3] != "Planned gym" {
		t.Fatalf("planned export: %v", rows)
	}
}

// Large exports page through the ledger: every row exactly once, in order.
func TestIntegration_ReportCSVStreamsAcrossPages(t *testing.T) {
	e := newEnv(t, testutil.CleanDB(t))
	alice := e.register("Alice", "alice-rep-big@test.local")
	e.useBase("USD", alice)
	acc := str(e.createAccount(alice, "Main", "USD", 0)["id"])
	const n = 2300 // > 2 pages of 1000
	if _, err := e.pool.Exec(context.Background(), `
		INSERT INTO transactions (user_id, type, account_id, amount, currency, title, occurred_at, source)
		SELECT $1, 'EXPENSE', $2, 100 + i, 'USD', 'bulk ' || i, TIMESTAMPTZ '2026-01-01' + (i / 7) * interval '1 hour', 'IMPORT'
		FROM generate_series(1, $3::int) i`, alice.id, acc, n); err != nil {
		t.Fatal(err)
	}
	status, raw := e.raw("GET", "/api/v1/reports/export.csv?exclude_source=ADJUSTMENT", alice.token, nil, nil)
	if status != 200 {
		t.Fatalf("status %d", status)
	}
	rows := readCSV(t, raw)[1:]
	if len(rows) != n {
		t.Fatalf("rows: %d", len(rows))
	}
	seen := map[string]bool{}
	for i, r := range rows {
		if seen[r[3]] {
			t.Fatalf("duplicate %s", r[3])
		}
		seen[r[3]] = true
		if i > 0 && rows[i-1][0] < r[0] {
			t.Fatalf("order broken at %d", i)
		}
	}
	// The report agrees.
	rep := e.must(200, "GET", "/api/v1/reports", alice, nil)
	if num(rep["count"]) != n {
		t.Fatalf("report count: %v", rep["count"])
	}
}

// ledgerSum pages /ledger (limit 2) with the given filters and sums what a report counts:
// positive-amount INCOME/EXPENSE entries that are neither adjustments nor loan bookings,
// converted to USD (fixture rates divide evenly, so per-entry and per-total rounding agree).
func ledgerSum(t *testing.T, e *env, u user, q string) (income, expense, count int64) {
	t.Helper()
	// Accounts excluded from the balance do not count unless the query names accounts (as /overview).
	excluded := map[string]bool{}
	if !strings.Contains(q, "account=") {
		for _, a := range list(e.must(200, "GET", "/api/v1/accounts", u, nil), "data") {
			if a["include_in_balance"] == false {
				excluded[str(a["id"])] = true
			}
		}
	}
	cursor := ""
	for pages := 0; pages < 200; pages++ {
		path := "/api/v1/ledger?limit=2&exclude_source=ADJUSTMENT,LOAN&" + q
		if cursor != "" {
			path += "&cursor=" + url.QueryEscape(cursor)
		}
		out := e.must(200, "GET", path, u, nil)
		for _, en := range list(out, "data") {
			typ := str(en["type"])
			amt := float64(num(en["amount"]))
			if amt == 0 || (typ != "INCOME" && typ != "EXPENSE") || excluded[str(en["account_id"])] {
				continue
			}
			switch str(en["currency"]) {
			case "NPR":
				amt /= 133
			case "EUR":
				amt /= 0.9
			}
			v := int64(math.Round(amt))
			if typ == "INCOME" {
				income += v
			} else {
				expense += v
			}
			count++
		}
		next, _ := out["next_cursor"].(string)
		if next == "" {
			return
		}
		cursor = next
	}
	t.Fatal("ledger paging did not terminate")
	return
}

// ledgerRows counts every row /ledger pages through for the filters (nothing excluded).
func ledgerRows(t *testing.T, e *env, u user, q string) int64 {
	t.Helper()
	var n int64
	cursor := ""
	for i := 0; i < 200; i++ {
		path := "/api/v1/ledger?limit=2&" + q
		if cursor != "" {
			path += "&cursor=" + url.QueryEscape(cursor)
		}
		out := e.must(200, "GET", path, u, nil)
		n += int64(len(list(out, "data")))
		if cursor, _ = out["next_cursor"].(string); cursor == "" {
			return n
		}
	}
	t.Fatal("paging")
	return 0
}

func TestIntegration_ReportMatchesLedgerPaging(t *testing.T) {
	f := newReportFixture(t)
	// An account excluded from the balance with an expense in range joins the matrix.
	vault := str(f.e.createAccount(f.alice, "Vault", "USD", 0)["id"])
	f.e.must(200, "PATCH", "/api/v1/accounts/"+vault, f.alice, map[string]any{"include_in_balance": false})
	f.e.must(201, "POST", "/api/v1/transactions", f.alice, map[string]any{"type": "EXPENSE", "account_id": vault, "amount": 7700, "title": "Vault spend", "occurred_at": "2026-05-12", "category_id": f.rent})
	queries := []string{
		may,
		"",
		may + "&type=EXPENSE",
		may + "&type=INCOME",
		may + "&category=" + f.food,
		may + "&category=none",
		may + "&category=none," + f.rent,
		may + "&account=" + f.bank,
		may + "&account=" + f.nepal,
		may + "&account=" + vault,
		may + "&account=" + f.bank + "," + vault,
		may + "&tag=" + f.t1,
		may + "&tag=" + f.t1 + "," + f.holiday,
		may + "&excl_tag=" + f.t1,
		may + "&q=lunch+pizza",
		may + "&q=holiday",
		may + "&q=holiday+paris",
		may + "&q=groceries",
		may + "&excl_q=pizza+paris",
		may + "&excl_q=groceries",
		may + "&excl_q=holiday",
		may + "&min=10000",
		may + "&max=9000",
		may + "&min=5000&max=13300",
		may + "&source=GROUP",
		may + "&source=MANUAL",
		may + "&exclude_source=GROUP",
		may + "&include_group_shares=false",
		may + "&type=TRANSFER",
		may + "&source=ADJUSTMENT",
		may + "&source=LOAN",
		may + "&planned=all",
		"from=2026-05-03&to=2026-05-06",
		"from=2026-05-06T12:00:00Z&to=2026-05-10",
		"from=2026-05-10T00:00:00%2B05:45&to=2026-05-11T00:00:00%2B05:45",
		"from=2026-05-10T00:00:30Z&to=2026-05-12T00:00:30Z",
	}
	for _, q := range queries {
		for _, u := range []user{f.alice, f.bob} {
			who := "alice"
			if u == f.bob {
				who = "bob"
			}
			r := f.report(u, q)
			inc, exp, cnt := ledgerSum(t, f.e, u, q)
			// The fixture's amounts divide evenly by the rates, so per-pair and per-entry rounding agree exactly.
			if num(r["income"]) != inc || num(r["expense"]) != exp || num(r["count"]) != cnt {
				t.Errorf("%s %q: report income/expense/count = %v/%v/%v, ledger paging = %d/%d/%d", who, q, r["income"], r["expense"], r["count"], inc, exp, cnt)
			}
			// entries_count is exactly the number of rows /ledger lists (and the CSV) for the same filters.
			if want := ledgerRows(t, f.e, u, q); num(r["entries_count"]) != want {
				t.Errorf("%s %q: entries_count %v, /ledger lists %d", who, q, r["entries_count"], want)
			}
			// Breakdowns add up to the totals.
			var catSum, accInc, accExp int64
			var pct float64
			for _, c := range list(r, "by_category") {
				catSum += num(c["amount"])
				pct += c["percent"].(float64)
			}
			for _, a := range list(r, "by_account") {
				accInc += num(a["base_income"])
				accExp += num(a["base_expense"])
			}
			if catSum != num(r["expense"]) || accInc != num(r["income"]) || accExp != num(r["expense"]) {
				t.Errorf("%s %q: by_category=%d by_account=%d/%d vs expense=%v income=%v", who, q, catSum, accInc, accExp, r["expense"], r["income"])
			}
			if len(list(r, "by_category")) > 0 && math.Abs(pct-100) > 1e-9 {
				t.Errorf("%s %q: percent sums to %v", who, q, pct)
			}
			if ri := f.report(u, q+"&breakdown=income"); func() int64 {
				var s int64
				for _, c := range list(ri, "by_category") {
					s += num(c["amount"])
				}
				return s
			}() != num(r["income"]) {
				t.Errorf("%s %q: income breakdown != income", who, q)
			}
			// The trend over the same window adds up too.
			if strings.Contains(q, "from=") {
				tr := f.e.must(200, "GET", "/api/v1/reports/trend?bucket=day&"+q, u, nil)
				var ti, te int64
				for _, b := range list(tr, "buckets") {
					ti += num(b["income"])
					te += num(b["expense"])
				}
				if ti != num(r["income"]) || te != num(r["expense"]) {
					t.Errorf("%s %q: trend %d/%d vs report %v/%v", who, q, ti, te, r["income"], r["expense"])
				}
			}
			// So does the CSV: same entries as /ledger.
			if q == may {
				_, raw := f.e.raw("GET", "/api/v1/reports/export.csv?"+q+"&exclude_source=ADJUSTMENT,LOAN", u.token, nil, nil)
				if n := len(readCSV(t, raw)) - 1; n < int(cnt) {
					t.Errorf("%s csv rows %d < counted entries %d", who, n, cnt)
				}
				_, raw = f.e.raw("GET", "/api/v1/reports/export.csv?"+q, u.token, nil, nil)
				if n := int64(len(readCSV(t, raw)) - 1); n != num(r["entries_count"]) {
					t.Errorf("%s csv rows %d != entries_count %v", who, n, r["entries_count"])
				}
			}
		}
	}
}
