package wallet_test

import (
	"testing"

	"github.com/settlr-org/settlr-api/internal/testutil"
)

func TestIntegration_BudgetsSpent(t *testing.T) {
	e := newEnv(t, testutil.CleanDB(t))
	alice := e.register("Alice", "alice-wallet-budget@test.local")
	bob := e.register("Bob", "bob-wallet-budget@test.local")
	e.makeFriends(alice.id, bob.id)
	food := e.systemCategory("Food")
	rent := e.systemCategory("Rent")
	bank := str(e.createAccount(alice, "Bank", "NPR", 100000)["id"])
	cash := str(e.createAccount(alice, "Cash2", "NPR", 0)["id"])
	hidden := str(e.must(201, "POST", "/api/v1/accounts", alice, map[string]any{"name": "Hidden", "currency": "NPR", "include_in_balance": false})["id"])
	bobBank := str(e.createAccount(bob, "Bank", "NPR", 0)["id"])

	g := e.must(201, "POST", "/api/v1/groups", alice, map[string]any{"name": "Flat", "currency": "NPR"})
	gid := str(g["id"])
	e.must(201, "POST", "/api/v1/groups/"+gid+"/members", alice, map[string]any{"user_id": bob.id.String()})
	split := []map[string]string{{"user_id": alice.id.String()}, {"user_id": bob.id.String()}}
	// Bob paid 6000 → my share 3000 (no account). I paid 10000 from Bank → my share 5000.
	e.must(201, "POST", "/api/v1/groups/"+gid+"/expenses", bob, map[string]any{
		"description": "Dinner", "amount": 6000, "paid_by": bob.id.String(), "split_mode": "EQUAL", "splits": split,
		"category_id": food, "expense_date": "2026-05-10"})
	e.must(201, "POST", "/api/v1/groups/"+gid+"/expenses", alice, map[string]any{
		"description": "Groceries", "amount": 10000, "paid_by": alice.id.String(), "split_mode": "EQUAL", "splits": split,
		"category_id": food, "expense_date": "2026-05-11", "payer_account_id": bank})

	txn := func(acct, cat string, typ string, amount int64, day string) {
		e.must(201, "POST", "/api/v1/transactions", alice, map[string]any{"type": typ, "account_id": acct, "category_id": cat, "amount": amount, "occurred_at": day})
	}
	txn(bank, food, "EXPENSE", 1000, "2026-05-05")
	txn(cash, rent, "EXPENSE", 2000, "2026-05-06")
	txn(hidden, food, "EXPENSE", 700, "2026-05-07")
	txn(bank, food, "INCOME", 9999, "2026-05-08")      // income never counts
	txn(bank, food, "EXPENSE", 4444, "2026-06-01")     // outside the range
	e.must(201, "POST", "/api/v1/transactions", alice, // planned items never count
		map[string]any{"type": "EXPENSE", "account_id": bank, "category_id": food, "amount": 555, "due_at": "2026-05-20"})
	e.must(201, "POST", "/api/v1/loans", alice, // loan bookings never count
		map[string]any{"name": "To Bob", "type": "LEND", "amount": 800, "account_id": bank, "occurred_at": "2026-05-09"})

	mk := func(body map[string]any) string {
		return str(e.must(201, "POST", "/api/v1/budgets", alice, body)["id"])
	}
	all := mk(map[string]any{"name": "All", "amount": 20000})
	foodB := mk(map[string]any{"name": "Food", "amount": 8000, "category_ids": []string{food}})
	foodBank := mk(map[string]any{"name": "Food@Bank", "amount": 10000, "category_ids": []string{food}, "account_ids": []string{bank}})
	foodHidden := mk(map[string]any{"name": "Food@Hidden", "amount": 1000, "category_ids": []string{food}, "account_ids": []string{hidden}})
	usd := mk(map[string]any{"name": "Food USD", "amount": 10000, "currency": "USD", "category_ids": []string{food}})

	res := e.must(200, "GET", "/api/v1/budgets?from=2026-05-01&to=2026-05-31", alice, nil)
	byID := map[string]map[string]any{}
	for _, b := range list(res, "data") {
		byID[str(b["id"])] = b
	}
	check := func(id string, spent int64) map[string]any {
		t.Helper()
		b := byID[id]
		if num(b["spent"]) != spent || num(b["remaining"]) != num(b["amount"])-spent {
			t.Fatalf("budget %s: spent %v want %d (%v)", b["name"], b["spent"], spent, b)
		}
		return b
	}
	check(all, 1000+2000+3000+5000) // hidden-account transactions left out, group shares in
	if b := check(foodB, 1000+3000+5000); b["percent"].(float64) != 112.5 || num(b["remaining"]) != -1000 {
		t.Fatalf("over budget: %v", b)
	}
	check(foodBank, 1000+5000) // a share counts only when I paid it from a listed account
	check(foodHidden, 700)     // an explicitly listed account counts even if excluded from the balance
	check(usd, 68)             // 9000 NPR minor = 90 NPR = 0.6767 USD → 68 cents
	if num(res["total_amount"]) != 20000+8000+10000+1000+1330000 || str(res["currency"]) != "NPR" {
		t.Fatalf("totals: %v", res)
	}
	if num(res["total_remaining"]) != num(res["total_amount"])-num(res["total_spent"]) {
		t.Fatalf("total_remaining: %v", res)
	}
	if got := list(e.must(200, "GET", "/api/v1/budgets?from=2026-07-01&to=2026-07-31", alice, nil), "data"); num(got[0]["spent"]) != 0 {
		t.Fatalf("empty range: %v", got[0])
	}
	if def := e.must(200, "GET", "/api/v1/budgets", alice, nil); def["from"] == nil || def["to"] == nil {
		t.Fatalf("default period: %v", def)
	}
	e.status(422, "GET", "/api/v1/budgets?from=2026-05-01", alice, nil)

	// Edit, get, reorder, delete.
	upd := e.must(200, "PATCH", "/api/v1/budgets/"+foodB+"?from=2026-05-01&to=2026-05-31", alice, map[string]any{"category_ids": []string{rent}, "name": "Rent"})
	if num(upd["spent"]) != 2000 || str(upd["name"]) != "Rent" {
		t.Fatalf("patched budget: %v", upd)
	}
	if got := e.must(200, "GET", "/api/v1/budgets/"+foodB, alice, nil); len(got["category_ids"].([]any)) != 1 {
		t.Fatalf("get budget: %v", got)
	}
	order := e.must(200, "POST", "/api/v1/budgets/reorder", alice, map[string]any{"ids": []string{usd, all, foodB, foodBank, foodHidden}})
	if got := list(order, "data"); str(got[0]["id"]) != usd || num(got[0]["sort_order"]) != 0 {
		t.Fatalf("reorder: %v", got[0])
	}
	e.must(200, "DELETE", "/api/v1/budgets/"+foodHidden, alice, nil)
	e.status(404, "GET", "/api/v1/budgets/"+foodHidden, alice, nil)

	// Validation and ownership.
	e.status(422, "POST", "/api/v1/budgets", alice, map[string]any{"name": "x", "amount": 0})
	e.status(422, "POST", "/api/v1/budgets", alice, map[string]any{"name": "x", "amount": 1, "account_ids": []string{bobBank}})
	e.status(422, "POST", "/api/v1/budgets", alice, map[string]any{"name": "x", "amount": 1, "category_ids": []string{bank}})
	e.status(422, "POST", "/api/v1/budgets", alice, map[string]any{"name": "x", "amount": 1, "currency": "XXX"})
	e.status(404, "GET", "/api/v1/budgets/"+all, bob, nil)
	e.status(404, "PATCH", "/api/v1/budgets/"+all, bob, map[string]any{"amount": 5})
	e.status(404, "DELETE", "/api/v1/budgets/"+all, bob, nil)
	e.status(422, "POST", "/api/v1/budgets/reorder", bob, map[string]any{"ids": []string{all}})
	if got := list(e.must(200, "GET", "/api/v1/budgets", bob, nil), "data"); len(got) != 0 {
		t.Fatalf("bob sees no budgets: %v", got)
	}

	// Idempotent create.
	hdr := map[string]string{"Idempotency-Key": "budget-create-1"}
	body := map[string]any{"name": "Once", "amount": 1}
	s1, b1 := e.raw("POST", "/api/v1/budgets", alice.token, body, hdr)
	s2, b2 := e.raw("POST", "/api/v1/budgets", alice.token, body, hdr)
	if s1 != 201 || s2 != 201 || string(b1) != string(b2) {
		t.Fatalf("idempotent budget: %d %d", s1, s2)
	}
	if got := list(e.must(200, "GET", "/api/v1/budgets", alice, nil), "data"); len(got) != 5 {
		t.Fatalf("idempotent create duplicated: %d", len(got))
	}

	// The legacy single monthly /personal/budget is untouched.
	e.must(200, "PUT", "/api/v1/personal/budget", alice, map[string]any{"amount": 5000, "currency": "NPR"})
	if pb := e.must(200, "GET", "/api/v1/personal/budget", alice, nil); num(pb["amount"]) != 5000 {
		t.Fatalf("personal budget: %v", pb)
	}
}
