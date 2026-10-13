package wallet_test

import (
	"testing"

	"github.com/settlr-org/settlr-api/internal/testutil"
)

func TestIntegration_AccountsBalancesTransfersAdjust(t *testing.T) {
	e := newEnv(t, testutil.CleanDB(t))
	alice := e.register("Alice", "alice-wallet-acc@test.local")
	mallory := e.register("Mallory", "mallory-wallet-acc@test.local")

	bank := e.createAccount(alice, "Bank", "NPR", 100000)
	if bank["is_default"] != true || num(bank["balance"]) != 100000 || str(bank["base_currency"]) != "NPR" {
		t.Fatalf("first account should be default with initial balance: %v", bank)
	}
	usd := e.createAccount(alice, "USD Card", "USD", 5000)
	if usd["is_default"] != false || num(usd["balance"]) != 5000 || num(usd["balance_base"]) != 5000*133 {
		t.Fatalf("usd account: %v", usd)
	}
	bankID, usdID := str(bank["id"]), str(usd["id"])

	// Validation: cross-currency transfer needs to_amount; same account; type rules.
	e.status(422, "POST", "/api/v1/transactions", alice, map[string]any{"type": "TRANSFER", "account_id": bankID, "to_account_id": usdID, "amount": 13300})
	e.status(422, "POST", "/api/v1/transactions", alice, map[string]any{"type": "TRANSFER", "account_id": bankID, "to_account_id": bankID, "amount": 100})
	e.status(422, "POST", "/api/v1/transactions", alice, map[string]any{"type": "INCOME", "account_id": bankID, "to_account_id": usdID, "amount": 100})
	e.status(422, "POST", "/api/v1/transactions", alice, map[string]any{"type": "EXPENSE", "account_id": bankID, "amount": 100, "currency": "USD"})
	e.status(422, "POST", "/api/v1/transactions", alice, map[string]any{"type": "EXPENSE", "account_id": bankID, "amount": 0})
	e.status(422, "POST", "/api/v1/transactions", alice, map[string]any{"type": "LOAN", "account_id": bankID, "amount": 10})
	e.status(422, "POST", "/api/v1/transactions", alice, map[string]any{"type": "EXPENSE", "amount": 10})
	e.status(400, "POST", "/api/v1/transactions", alice, map[string]any{"type": "EXPENSE", "account_id": bankID, "amount": 10, "bogus": 1})

	tr := e.must(201, "POST", "/api/v1/transactions", alice, map[string]any{
		"type": "TRANSFER", "account_id": bankID, "to_account_id": usdID, "amount": 13300, "to_amount": 100, "title": "To card",
	})
	if str(tr["currency"]) != "NPR" || num(tr["to_amount"]) != 100 {
		t.Fatalf("transfer: %v", tr)
	}
	// Same-currency transfer: to_amount defaults to amount and must match when given.
	cash := e.createAccount(alice, "Cash", "NPR", 0)
	e.status(422, "POST", "/api/v1/transactions", alice, map[string]any{"type": "TRANSFER", "account_id": bankID, "to_account_id": str(cash["id"]), "amount": 500, "to_amount": 400})
	same := e.must(201, "POST", "/api/v1/transactions", alice, map[string]any{"type": "TRANSFER", "account_id": bankID, "to_account_id": str(cash["id"]), "amount": 500})
	if num(same["to_amount"]) != 500 {
		t.Fatalf("same-currency to_amount should default to amount: %v", same)
	}

	accs := e.accountsByName(alice)
	if num(accs["Bank"]["balance"]) != 100000-13300-500 || num(accs["USD Card"]["balance"]) != 5100 || num(accs["Cash"]["balance"]) != 500 {
		t.Fatalf("balances after transfers: bank=%v usd=%v cash=%v", accs["Bank"]["balance"], accs["USD Card"]["balance"], accs["Cash"]["balance"])
	}
	if num(accs["USD Card"]["balance_base"]) != 5100*133 {
		t.Fatalf("usd balance_base: %v", accs["USD Card"]["balance_base"])
	}

	// Adjust Bank to 90000 -> ADJUSTMENT income of the difference.
	adj := e.must(200, "POST", "/api/v1/accounts/"+bankID+"/adjust", alice, map[string]any{"balance": 90000})
	at := adj["transaction"].(map[string]any)
	if str(at["source"]) != "ADJUSTMENT" || str(at["type"]) != "INCOME" || num(at["amount"]) != 90000-86200 {
		t.Fatalf("adjust txn: %v", at)
	}
	if num(adj["account"].(map[string]any)["balance"]) != 90000 {
		t.Fatalf("adjusted balance: %v", adj["account"])
	}
	noop := e.must(200, "POST", "/api/v1/accounts/"+bankID+"/adjust", alice, map[string]any{"balance": 90000})
	if noop["transaction"] != nil {
		t.Fatalf("adjust to same balance should not post: %v", noop)
	}
	e.status(422, "POST", "/api/v1/accounts/"+bankID+"/adjust", alice, map[string]any{})

	// Overview: balance in base currency over included accounts; transfers are not flows.
	ov := e.must(200, "GET", "/api/v1/overview?from=2000-01-01&to=2100-01-01", alice, nil)
	if num(ov["balance"]) != 90000+5100*133+500 {
		t.Fatalf("overview balance: %v", ov)
	}
	if num(ov["income"]) != 100000+5000*133+3800 || num(ov["expense"]) != 0 {
		t.Fatalf("overview income/expense: %v", ov)
	}
	if len(list(ov, "upcoming")) != 0 || len(list(ov, "overdue")) != 0 {
		t.Fatalf("upcoming/overdue should be empty arrays: %v", ov)
	}
	// Excluding an account from the balance drops it from the total.
	e.must(200, "PATCH", "/api/v1/accounts/"+str(cash["id"]), alice, map[string]any{"include_in_balance": false})
	ov = e.must(200, "GET", "/api/v1/overview", alice, nil)
	if num(ov["balance"]) != 90000+5100*133 {
		t.Fatalf("overview balance without cash: %v", ov["balance"])
	}

	// Account summary: transfers count as inflow, and as income only when the setting says so.
	sum := e.must(200, "GET", "/api/v1/accounts/"+usdID+"/summary", alice, nil)
	if num(sum["income"]) != 5000 || num(sum["inflow"]) != 5100 || num(sum["balance"]) != 5100 || len(list(sum, "entries")) != 2 {
		t.Fatalf("usd summary: %v", sum)
	}
	e.must(200, "PATCH", "/api/v1/me/settings", alice, map[string]any{"treat_transfers_as_flow": true})
	sum = e.must(200, "GET", "/api/v1/accounts/"+usdID+"/summary?from=2000-01-01", alice, nil)
	if num(sum["income"]) != 5100 {
		t.Fatalf("usd summary with transfers as flow: %v", sum)
	}

	// Patch, currency lock, default switching, reorder.
	e.status(422, "PATCH", "/api/v1/accounts/"+usdID, alice, map[string]any{"currency": "EUR"})
	empty := e.createAccount(alice, "Empty", "NPR", 0)
	p := e.must(200, "PATCH", "/api/v1/accounts/"+str(empty["id"]), alice, map[string]any{"currency": "EUR", "name": "Euro", "archived": true, "is_default": true})
	if str(p["currency"]) != "EUR" || p["archived"] != true || p["is_default"] != true {
		t.Fatalf("patch account: %v", p)
	}
	if e.accountsByName(alice)["Bank"]["is_default"] != false {
		t.Fatal("old default should be cleared")
	}
	ordered := e.must(200, "POST", "/api/v1/accounts/reorder", alice, map[string]any{"ids": []string{usdID, str(cash["id"]), bankID, str(empty["id"])}})
	if names := list(ordered, "data"); str(names[0]["name"]) != "USD Card" || str(names[2]["name"]) != "Bank" {
		t.Fatalf("reorder: %v", names)
	}
	e.status(422, "POST", "/api/v1/accounts/reorder", alice, map[string]any{"ids": []string{bankID, bankID}})

	// Other users' accounts are invisible.
	e.status(404, "GET", "/api/v1/accounts/"+bankID, mallory, nil)
	e.status(404, "PATCH", "/api/v1/accounts/"+bankID, mallory, map[string]any{"name": "x"})
	e.status(404, "DELETE", "/api/v1/accounts/"+bankID, mallory, nil)
	e.status(404, "POST", "/api/v1/accounts/"+bankID+"/adjust", mallory, map[string]any{"balance": 1})
	e.status(404, "GET", "/api/v1/accounts/"+bankID+"/summary", mallory, nil)
	e.status(422, "POST", "/api/v1/transactions", mallory, map[string]any{"type": "EXPENSE", "account_id": bankID, "amount": 10})
	e.status(422, "POST", "/api/v1/accounts/reorder", mallory, map[string]any{"ids": []string{bankID}})

	// Deleting the USD account removes its transactions (incl. the transfer) from balances.
	e.status(200, "DELETE", "/api/v1/accounts/"+usdID, alice, nil)
	e.status(404, "GET", "/api/v1/accounts/"+usdID, alice, nil)
	e.status(404, "GET", "/api/v1/transactions/"+str(tr["id"]), alice, nil)
	if b := num(e.accountsByName(alice)["Bank"]["balance"]); b != 90000+13300 {
		t.Fatalf("bank after deleting usd account: %d", b)
	}

	// Deleting the default account promotes another one.
	e.status(200, "DELETE", "/api/v1/accounts/"+str(empty["id"]), alice, nil)
	defaults := 0
	for _, a := range e.accountsByName(alice) {
		if a["is_default"] == true {
			defaults++
		}
	}
	if defaults != 1 {
		t.Fatalf("expected exactly one default account, got %d", defaults)
	}
}

func TestIntegration_SettingsAndRateOverrides(t *testing.T) {
	e := newEnv(t, testutil.CleanDB(t))
	alice := e.register("Alice", "alice-wallet-set@test.local")

	s := e.must(200, "GET", "/api/v1/me/settings", alice, nil)
	if num(s["month_start_day"]) != 1 || str(s["theme"]) != "auto" || str(s["reminder_time"]) != "20:00" || s["onboarded_at"] != nil {
		t.Fatalf("default settings: %v", s)
	}
	s = e.must(200, "PATCH", "/api/v1/me/settings", alice, map[string]any{
		"month_start_day": 25, "theme": "amoled", "hide_balance": true, "reminder_enabled": true,
		"reminder_time": "08:30", "onboarded_at": "2026-10-01T10:00:00Z",
	})
	if num(s["month_start_day"]) != 25 || str(s["theme"]) != "amoled" || s["hide_balance"] != true || str(s["reminder_time"]) != "08:30" || s["onboarded_at"] == nil {
		t.Fatalf("patched settings: %v", s)
	}
	for _, bad := range []map[string]any{
		{"month_start_day": 29}, {"month_start_day": 0}, {"theme": "pink"}, {"reminder_time": "24:00"}, {"onboarded_at": "yesterday"},
	} {
		e.status(422, "PATCH", "/api/v1/me/settings", alice, bad)
	}
	s = e.must(200, "PATCH", "/api/v1/me/settings", alice, map[string]any{"onboarded_at": nil})
	if s["onboarded_at"] != nil || num(s["month_start_day"]) != 25 {
		t.Fatalf("partial patch: %v", s)
	}

	usd := e.createAccount(alice, "USD", "USD", 1000)
	if num(usd["balance_base"]) != 133000 {
		t.Fatalf("provider rate: %v", usd["balance_base"])
	}
	r := e.must(200, "PUT", "/api/v1/me/rates/usd/NPR", alice, map[string]any{"rate": 140.5})
	if str(r["base"]) != "USD" || str(r["quote"]) != "NPR" || r["rate"].(float64) != 140.5 {
		t.Fatalf("put rate: %v", r)
	}
	if b := num(e.accountsByName(alice)["USD"]["balance_base"]); b != 140500 {
		t.Fatalf("override not applied: %d", b)
	}
	// The inverse override is used for the opposite direction.
	e.status(200, "DELETE", "/api/v1/me/rates/USD/NPR", alice, nil)
	e.must(200, "PUT", "/api/v1/me/rates/NPR/USD", alice, map[string]any{"rate": 0.01})
	if b := num(e.accountsByName(alice)["USD"]["balance_base"]); b != 100000 {
		t.Fatalf("inverse override: %d", b)
	}
	if rs := list(e.must(200, "GET", "/api/v1/me/rates", alice, nil), "data"); len(rs) != 1 || str(rs[0]["base"]) != "NPR" {
		t.Fatalf("list rates: %v", rs)
	}
	e.status(422, "PUT", "/api/v1/me/rates/USD/USD", alice, map[string]any{"rate": 1})
	e.status(422, "PUT", "/api/v1/me/rates/XXX/USD", alice, map[string]any{"rate": 1})
	e.status(422, "PUT", "/api/v1/me/rates/EUR/USD", alice, map[string]any{"rate": -1})
	e.status(404, "DELETE", "/api/v1/me/rates/EUR/USD", alice, nil)

	// A currency without any rate is reported instead of silently miscounted.
	e.status(200, "DELETE", "/api/v1/me/rates/NPR/USD", alice, nil)
	e.createAccount(alice, "Rand", "ZAR", 500)
	ov := e.must(200, "GET", "/api/v1/overview", alice, nil)
	if num(ov["balance"]) != 133000 || len(ov["missing_rates"].([]any)) != 1 {
		t.Fatalf("overview with missing rate: %v", ov)
	}
	if a := e.accountsByName(alice)["Rand"]; a["balance_base"] != nil || num(a["balance"]) != 500 {
		t.Fatalf("unconvertible balance_base should be null: %v", a)
	}
}
