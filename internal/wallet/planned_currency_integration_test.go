package wallet_test

import (
	"testing"
	"time"

	"github.com/settlr-org/settlr-api/internal/testutil"
)

func TestIntegration_PayIntoOtherCurrency(t *testing.T) {
	e := newEnv(t, testutil.CleanDB(t))
	alice := e.register("Alice", "alice-wallet-fxpay@test.local")
	npr := str(e.createAccount(alice, "Bank", "NPR", 0)["id"])
	usd := str(e.createAccount(alice, "Dollars", "USD", 0)["id"])
	eur := str(e.createAccount(alice, "Euro", "EUR", 0)["id"])
	jpy := str(e.createAccount(alice, "Yen", "JPY", 0)["id"])
	planned := func(body map[string]any) string {
		t.Helper()
		body["due_at"] = "2030-01-01"
		return str(e.must(201, "POST", "/api/v1/transactions", alice, body)["id"])
	}
	paid := func(id string, body map[string]any) map[string]any {
		t.Helper()
		return asMap(e.must(200, "POST", "/api/v1/transactions/"+id+"/pay", alice, body)["transaction"])
	}

	// No amount: converted (1500.00 NPR at 133 NPR/USD = 11.28 USD).
	got := paid(planned(map[string]any{"type": "EXPENSE", "account_id": npr, "amount": 150000}), map[string]any{"account_id": usd})
	if str(got["currency"]) != "USD" || num(got["amount"]) != 1128 || str(got["account_id"]) != usd {
		t.Fatalf("converted pay: %v", got)
	}
	// An account-less item carries its own currency and converts the same way.
	got = paid(planned(map[string]any{"type": "INCOME", "amount": 150000, "currency": "NPR"}), map[string]any{"account_id": usd})
	if str(got["currency"]) != "USD" || num(got["amount"]) != 1128 {
		t.Fatalf("converted pay (no account): %v", got)
	}
	// With amount: taken as account-currency minor units.
	got = paid(planned(map[string]any{"type": "EXPENSE", "account_id": npr, "amount": 150000}), map[string]any{"account_id": usd, "amount": 1100})
	if str(got["currency"]) != "USD" || num(got["amount"]) != 1100 {
		t.Fatalf("explicit amount: %v", got)
	}
	// Same currency: unchanged.
	npr2 := str(e.createAccount(alice, "Bank2", "NPR", 0)["id"])
	got = paid(planned(map[string]any{"type": "EXPENSE", "account_id": npr, "amount": 777}), map[string]any{"account_id": npr2})
	if str(got["currency"]) != "NPR" || num(got["amount"]) != 777 {
		t.Fatalf("same-currency pay: %v", got)
	}
	// No rate NPR→JPY: 422 RATES_UNAVAILABLE and the item stays planned.
	id := planned(map[string]any{"type": "EXPENSE", "account_id": npr, "amount": 150000})
	s, out := e.do("POST", "/api/v1/transactions/"+id+"/pay", alice.token, map[string]any{"account_id": jpy})
	if s != 422 || str(asMap(out["error"])["code"]) != "RATES_UNAVAILABLE" {
		t.Fatalf("missing rate: %d %v", s, out)
	}
	if tx := e.must(200, "GET", "/api/v1/transactions/"+id, alice, nil); tx["planned"] != true {
		t.Fatalf("still planned: %v", tx)
	}
	// ...unless the amount is sent.
	if got = paid(id, map[string]any{"account_id": jpy, "amount": 1700}); str(got["currency"]) != "JPY" || num(got["amount"]) != 1700 {
		t.Fatalf("explicit amount without rate: %v", got)
	}

	// Transfers: the source amount converts; to_amount (EUR) keeps its value.
	tr := planned(map[string]any{"type": "TRANSFER", "account_id": npr, "to_account_id": eur, "amount": 13300, "to_amount": 90})
	got = paid(tr, map[string]any{"account_id": usd})
	if str(got["currency"]) != "USD" || num(got["amount"]) != 100 || num(got["to_amount"]) != 90 {
		t.Fatalf("transfer pay: %v", got)
	}
	// Into the destination's currency: to_amount follows the converted amount.
	tr = planned(map[string]any{"type": "TRANSFER", "account_id": npr, "to_account_id": usd, "amount": 13300, "to_amount": 100})
	got = paid(tr, map[string]any{"account_id": str(e.createAccount(alice, "Dollars2", "USD", 0)["id"])})
	if num(got["amount"]) != 100 || num(got["to_amount"]) != 100 {
		t.Fatalf("transfer into same currency: %v", got)
	}

	// PATCH moving a pending item to another-currency account converts too.
	id = planned(map[string]any{"type": "EXPENSE", "account_id": npr, "amount": 150000})
	if p := e.must(200, "PATCH", "/api/v1/transactions/"+id, alice, map[string]any{"account_id": usd}); str(p["currency"]) != "USD" || num(p["amount"]) != 1128 || p["planned"] != true {
		t.Fatalf("patched pending: %v", p)
	}
	id = planned(map[string]any{"type": "EXPENSE", "account_id": npr, "amount": 150000})
	p := e.must(200, "PATCH", "/api/v1/transactions/"+id, alice, map[string]any{"account_id": usd, "occurred_at": "2030-01-02"})
	if str(p["currency"]) != "USD" || num(p["amount"]) != 1128 || p["planned"] != false {
		t.Fatalf("patched pending to paid: %v", p)
	}
	e.status(422, "PATCH", "/api/v1/transactions/"+planned(map[string]any{"type": "EXPENSE", "account_id": npr, "amount": 1}), alice, map[string]any{"account_id": jpy})
	if b := num(e.accountsByName(alice)["Dollars"]["balance"]); b != -1128+1128-1100-100+100-1128 { // expense, income, explicit, transfer out, transfer in, patched-paid
		t.Fatalf("USD balance: %d", b)
	}
}

func TestIntegration_SkipOverdueFilters(t *testing.T) {
	e := newEnv(t, testutil.CleanDB(t))
	alice := e.register("Alice", "alice-wallet-skipf@test.local")
	bank := str(e.createAccount(alice, "Bank", "NPR", 0)["id"])
	cash := str(e.createAccount(alice, "Cash2", "NPR", 0)["id"])
	food := e.systemCategory("Food")
	past := time.Now().UTC().Add(-24 * time.Hour).Format(time.RFC3339)
	mk := func(body map[string]any) string {
		body["due_at"] = past
		body["amount"] = 10
		return str(e.must(201, "POST", "/api/v1/transactions", alice, body)["id"])
	}
	onBank := mk(map[string]any{"type": "EXPENSE", "account_id": bank})
	onCashFood := mk(map[string]any{"type": "EXPENSE", "account_id": cash, "category_id": food})
	transferToCash := mk(map[string]any{"type": "TRANSFER", "account_id": bank, "to_account_id": cash})
	bankFood := mk(map[string]any{"type": "EXPENSE", "account_id": bank, "category_id": food})

	gone := func(id string) bool { return e.deletedAt("transactions", id) != nil }
	if res := e.must(200, "POST", "/api/v1/transactions/skip-overdue?account="+cash+"&category="+food, alice, nil); num(res["skipped"]) != 1 || !gone(onCashFood) {
		t.Fatalf("account+category: %v", res)
	}
	if res := e.must(200, "POST", "/api/v1/transactions/skip-overdue?account="+cash, alice, nil); num(res["skipped"]) != 1 || !gone(transferToCash) {
		t.Fatalf("account matches the transfer destination: %v", res)
	}
	if res := e.must(200, "POST", "/api/v1/transactions/skip-overdue?category="+food, alice, nil); num(res["skipped"]) != 1 || !gone(bankFood) || gone(onBank) {
		t.Fatalf("category: %v", res)
	}
	e.status(422, "POST", "/api/v1/transactions/skip-overdue?account=nope", alice, nil)
	if res := e.must(200, "POST", "/api/v1/transactions/skip-overdue", alice, nil); num(res["skipped"]) != 1 || !gone(onBank) {
		t.Fatalf("unfiltered: %v", res)
	}
}
