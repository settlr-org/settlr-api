package wallet_test

import (
	"testing"

	"github.com/settlr-org/settlr-api/internal/testutil"
)

func TestIntegration_LoansAndRecords(t *testing.T) {
	e := newEnv(t, testutil.CleanDB(t))
	alice := e.register("Alice", "alice-wallet-loan@test.local")
	bob := e.register("Bob", "bob-wallet-loan@test.local")
	bank := str(e.createAccount(alice, "Bank", "NPR", 100000)["id"])
	bobBank := str(e.createAccount(bob, "Bank", "NPR", 0)["id"])
	balance := func() int64 { t.Helper(); return num(e.accountsByName(alice)["Bank"]["balance"]) }
	period := "?from=2026-01-01&to=2027-01-01"
	noFlows := func() {
		t.Helper()
		ov := e.must(200, "GET", "/api/v1/overview"+period, alice, nil)
		if num(ov["income"]) != 0 || num(ov["expense"]) != 0 {
			t.Fatalf("loan bookings must not be income/expense: %v", ov)
		}
		s := e.must(200, "GET", "/api/v1/accounts/"+bank+"/summary"+period, alice, nil)
		if num(s["income"]) != 0 || num(s["expense"]) != 0 {
			t.Fatalf("account summary flows: %v", s)
		}
	}

	// LEND from Bank: money out.
	loan := e.must(201, "POST", "/api/v1/loans", alice, map[string]any{
		"name": "Bob", "type": "LEND", "amount": 20000, "account_id": bank, "occurred_at": "2026-05-01", "note": "rent help",
	})
	lid := str(loan["id"])
	booking := str(loan["transaction_id"])
	if str(loan["currency"]) != "NPR" || num(loan["remaining"]) != 20000 || loan["paid_off"] != false || booking == "" {
		t.Fatalf("created loan: %v", loan)
	}
	if b := balance(); b != 80000 {
		t.Fatalf("lend moves money out: %d", b)
	}
	noFlows()
	tx := e.must(200, "GET", "/api/v1/transactions/"+booking, alice, nil)
	if str(tx["source"]) != "LOAN" || str(tx["loan_id"]) != lid || str(tx["type"]) != "EXPENSE" {
		t.Fatalf("booking: %v", tx)
	}
	for _, en := range list(e.must(200, "GET", "/api/v1/ledger", alice, nil), "data") {
		if str(en["id"]) == booking && (en["read_only"] != true || str(en["loan_id"]) != lid) {
			t.Fatalf("ledger booking: %v", en)
		}
	}
	// Loan bookings are only changed through the loan.
	for _, c := range []struct{ method, path string }{
		{"PATCH", "/api/v1/transactions/" + booking}, {"DELETE", "/api/v1/transactions/" + booking},
		{"POST", "/api/v1/transactions/" + booking + "/duplicate"}, {"POST", "/api/v1/transactions/" + booking + "/pay"},
		{"POST", "/api/v1/transactions/" + booking + "/skip"},
	} {
		s, out := e.do(c.method, c.path, alice.token, map[string]any{"amount": 1})
		if s != 409 || str(asMap(out["error"])["code"]) != "LOAN_MANAGED" {
			t.Fatalf("%s %s: %d %v", c.method, c.path, s, out)
		}
	}
	if got := list(e.must(200, "GET", "/api/v1/personal/expenses", alice, nil), "data"); len(got) != 0 {
		t.Fatalf("loan bookings are not personal expenses: %v", got)
	}

	// Records: DECREASE with account = money back in; INCREASE without account.
	r1 := e.must(201, "POST", "/api/v1/loans/"+lid+"/records", alice, map[string]any{"type": "DECREASE", "amount": 5000, "account_id": bank, "occurred_at": "2026-06-01"})
	if r1["transaction_id"] == nil || str(r1["loan_id"]) != lid {
		t.Fatalf("record: %v", r1)
	}
	if b := balance(); b != 85000 {
		t.Fatalf("repayment moves money in: %d", b)
	}
	r2 := e.must(201, "POST", "/api/v1/loans/"+lid+"/records", alice, map[string]any{"type": "INCREASE", "amount": 1000, "note": "extra"})
	if r2["transaction_id"] != nil || balance() != 85000 {
		t.Fatalf("record without account: %v", r2)
	}
	got := e.must(200, "GET", "/api/v1/loans/"+lid, alice, nil)
	if num(got["total"]) != 21000 || num(got["paid"]) != 5000 || num(got["remaining"]) != 16000 || num(got["records_count"]) != 2 {
		t.Fatalf("loan totals: %v", got)
	}
	noFlows()
	// Edits keep the booking in sync.
	e.must(200, "PATCH", "/api/v1/loan-records/"+str(r1["id"]), alice, map[string]any{"amount": 6000})
	if b := balance(); b != 86000 {
		t.Fatalf("record amount synced: %d", b)
	}
	if bt := e.must(200, "GET", "/api/v1/transactions/"+str(r1["transaction_id"]), alice, nil); num(bt["amount"]) != 6000 || str(bt["loan_record_id"]) != str(r1["id"]) || str(bt["type"]) != "INCOME" {
		t.Fatalf("record booking: %v", bt)
	}
	upd := e.must(200, "PATCH", "/api/v1/loan-records/"+str(r1["id"]), alice, map[string]any{"account_id": nil})
	if upd["transaction_id"] != nil || balance() != 80000 {
		t.Fatalf("record account removed: %v %d", upd, balance())
	}
	e.status(404, "GET", "/api/v1/transactions/"+str(r1["transaction_id"]), alice, nil)
	e.must(200, "PATCH", "/api/v1/loan-records/"+str(r1["id"]), alice, map[string]any{"account_id": bank})
	if b := balance(); b != 86000 {
		t.Fatalf("record account re-added: %d", b)
	}
	if recs := list(e.must(200, "GET", "/api/v1/loans/"+lid+"/records", alice, nil), "data"); len(recs) != 2 || str(recs[0]["id"]) != str(r2["id"]) {
		t.Fatalf("records newest first: %v", recs)
	}

	// Pay it off.
	e.must(201, "POST", "/api/v1/loans/"+lid+"/records", alice, map[string]any{"type": "DECREASE", "amount": 15000})
	got = e.must(200, "GET", "/api/v1/loans/"+lid, alice, nil)
	if got["paid_off"] != true || num(got["remaining"]) != 0 || got["percent"].(float64) != 100 {
		t.Fatalf("paid off: %v", got)
	}

	// BORROW without account; adding the account later books money in.
	borrow := e.must(201, "POST", "/api/v1/loans", alice, map[string]any{"name": "Bank loan", "type": "BORROW", "amount": 3000})
	if borrow["transaction_id"] != nil || balance() != 86000 {
		t.Fatalf("borrow without account: %v", borrow)
	}
	borrow = e.must(200, "PATCH", "/api/v1/loans/"+str(borrow["id"]), alice, map[string]any{"account_id": bank})
	if borrow["transaction_id"] == nil || balance() != 89000 {
		t.Fatalf("borrow booked: %v %d", borrow, balance())
	}
	noFlows()
	ls := e.must(200, "GET", "/api/v1/loans?include_paid_off=false", alice, nil)
	if d := list(ls, "data"); len(d) != 1 || str(d[0]["type"]) != "BORROW" || num(ls["borrow_remaining"]) != 3000 || num(ls["lend_remaining"]) != 0 {
		t.Fatalf("list without paid off: %v", ls)
	}
	if d := list(e.must(200, "GET", "/api/v1/loans?type=LEND", alice, nil), "data"); len(d) != 1 || str(d[0]["id"]) != lid {
		t.Fatalf("type filter: %v", d)
	}
	// Flipping the type flips every booking: loan +20000 in, record −6000 out.
	e.must(200, "PATCH", "/api/v1/loans/"+lid, alice, map[string]any{"type": "BORROW"})
	if b := balance(); b != 89000+20000+20000-6000-6000 {
		t.Fatalf("type flip: %d", b)
	}

	// Multi-currency: USD loan on an NPR account converts (or takes account_amount).
	usd := e.must(201, "POST", "/api/v1/loans", alice, map[string]any{"name": "Trip", "type": "LEND", "amount": 100, "currency": "USD", "account_id": bank})
	if bt := e.must(200, "GET", "/api/v1/transactions/"+str(usd["transaction_id"]), alice, nil); num(bt["amount"]) != 13300 || str(bt["currency"]) != "NPR" {
		t.Fatalf("converted booking: %v", bt)
	}
	usdRec := e.must(201, "POST", "/api/v1/loans/"+str(usd["id"])+"/records", alice, map[string]any{"type": "DECREASE", "amount": 50, "account_id": bank, "account_amount": 7000})
	if bt := e.must(200, "GET", "/api/v1/transactions/"+str(usdRec["transaction_id"]), alice, nil); num(bt["amount"]) != 7000 {
		t.Fatalf("explicit account_amount: %v", bt)
	}

	// Ownership and validation.
	e.status(404, "GET", "/api/v1/loans/"+lid, bob, nil)
	e.status(404, "PATCH", "/api/v1/loans/"+lid, bob, map[string]any{"name": "x"})
	e.status(404, "DELETE", "/api/v1/loans/"+lid, bob, nil)
	e.status(404, "GET", "/api/v1/loans/"+lid+"/records", bob, nil)
	e.status(404, "POST", "/api/v1/loans/"+lid+"/records", bob, map[string]any{"type": "DECREASE", "amount": 1})
	e.status(404, "PATCH", "/api/v1/loan-records/"+str(r1["id"]), bob, map[string]any{"amount": 1})
	e.status(404, "DELETE", "/api/v1/loan-records/"+str(r1["id"]), bob, nil)
	e.status(422, "POST", "/api/v1/loans", alice, map[string]any{"name": "x", "type": "LEND", "amount": 1, "account_id": bobBank})
	e.status(422, "POST", "/api/v1/loans/"+lid+"/records", alice, map[string]any{"type": "DECREASE", "amount": 1, "account_id": bobBank})
	e.status(422, "POST", "/api/v1/loans", alice, map[string]any{"name": "x", "type": "GIFT", "amount": 1})
	e.status(422, "POST", "/api/v1/loans/"+lid+"/records", alice, map[string]any{"type": "DECREASE", "amount": 0})
	if d := list(e.must(200, "GET", "/api/v1/loans", bob, nil), "data"); len(d) != 0 {
		t.Fatalf("bob sees no loans: %v", d)
	}

	// Reorder, delete record, delete loan (bookings go with it).
	order := e.must(200, "POST", "/api/v1/loans/reorder", alice, map[string]any{"ids": []string{str(usd["id"]), lid, str(borrow["id"])}})
	if d := list(order, "data"); str(d[0]["id"]) != str(usd["id"]) {
		t.Fatalf("reorder: %v", d)
	}
	before := balance()
	e.must(200, "DELETE", "/api/v1/loan-records/"+str(usdRec["id"]), alice, nil)
	if b := balance(); b != before-7000 {
		t.Fatalf("record delete removes its booking: %d", b)
	}
	e.must(200, "DELETE", "/api/v1/loans/"+lid, alice, nil)
	e.must(200, "DELETE", "/api/v1/loans/"+str(usd["id"]), alice, nil)
	e.must(200, "DELETE", "/api/v1/loans/"+str(borrow["id"]), alice, nil)
	if b := balance(); b != 100000 {
		t.Fatalf("all bookings removed: %d", b)
	}
	e.status(404, "GET", "/api/v1/transactions/"+booking, alice, nil)
	e.status(404, "GET", "/api/v1/loans/"+lid, alice, nil)

	// Idempotent create.
	hdr := map[string]string{"Idempotency-Key": "loan-create-1"}
	body := map[string]any{"name": "Once", "type": "LEND", "amount": 10, "account_id": bank}
	s1, b1 := e.raw("POST", "/api/v1/loans", alice.token, body, hdr)
	s2, b2 := e.raw("POST", "/api/v1/loans", alice.token, body, hdr)
	if s1 != 201 || s2 != 201 || string(b1) != string(b2) || balance() != 99990 {
		t.Fatalf("idempotent loan: %d %d %d", s1, s2, balance())
	}

	// Deleting the account unlinks loans and removes their bookings.
	e.must(200, "DELETE", "/api/v1/accounts/"+bank, alice, nil)
	for _, l := range list(e.must(200, "GET", "/api/v1/loans", alice, nil), "data") {
		if l["account_id"] != nil || l["transaction_id"] != nil {
			t.Fatalf("loan still linked to deleted account: %v", l)
		}
	}
}
