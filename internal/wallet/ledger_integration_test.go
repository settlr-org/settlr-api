package wallet_test

import (
	"testing"

	"github.com/settlr-org/settlr-api/internal/testutil"
)

func TestIntegration_LedgerGroupSharesSettlementsAndCategories(t *testing.T) {
	e := newEnv(t, testutil.CleanDB(t))
	alice := e.register("Alice", "alice-wallet-led@test.local")
	bob := e.register("Bob", "bob-wallet-led@test.local")
	carol := e.register("Carol", "carol-wallet-led@test.local")
	e.makeFriends(alice.id, bob.id)

	g := e.must(201, "POST", "/api/v1/groups", alice, map[string]any{"name": "Flat", "currency": "NPR"})
	gid := str(g["id"])
	e.must(201, "POST", "/api/v1/groups/"+gid+"/members", alice, map[string]any{"user_id": bob.id.String()})

	aBank := str(e.createAccount(alice, "Bank", "NPR", 0)["id"])
	bWallet := str(e.createAccount(bob, "Wallet", "NPR", 0)["id"])
	food := e.systemCategory("Food")

	expense := func(u user, paidBy user, amount int64, splitWith []user, extra map[string]any) (int, map[string]any) {
		splits := []map[string]string{}
		for _, s := range splitWith {
			splits = append(splits, map[string]string{"user_id": s.id.String()})
		}
		body := map[string]any{"description": "Groceries", "amount": amount, "paid_by": paidBy.id.String(), "split_mode": "EQUAL", "splits": splits, "category_id": food, "expense_date": "2026-05-10"}
		for k, v := range extra {
			body[k] = v
		}
		return e.do("POST", "/api/v1/groups/"+gid+"/expenses", u.token, body)
	}

	// Only the payer may name the account, and only one of their own.
	if s, _ := expense(bob, alice, 10000, []user{alice, bob}, map[string]any{"payer_account_id": bWallet}); s != 403 {
		t.Fatalf("non-payer setting payer_account_id: %d", s)
	}
	if s, _ := expense(alice, alice, 10000, []user{alice, bob}, map[string]any{"payer_account_id": bWallet}); s != 422 {
		t.Fatalf("foreign payer_account_id: %d", s)
	}
	s, exp1 := expense(alice, alice, 10000, []user{alice, bob}, map[string]any{"payer_account_id": aBank})
	if s != 201 || str(exp1["payer_account_id"]) != aBank {
		t.Fatalf("expense with payer account: %d %v", s, exp1)
	}
	_, exp2 := expense(bob, bob, 6000, []user{alice, bob}, nil)
	_, exp3 := expense(alice, alice, 4000, []user{bob}, map[string]any{"payer_account_id": aBank})
	exp1ID, exp3ID := str(exp1["id"]), str(exp3["id"])

	// The payer account is only shown to the payer.
	if got := e.must(200, "GET", "/api/v1/expenses/"+exp1ID, alice, nil); str(got["payer_account_id"]) != aBank {
		t.Fatalf("payer sees account: %v", got["payer_account_id"])
	}
	if got := e.must(200, "GET", "/api/v1/expenses/"+exp1ID, bob, nil); got["payer_account_id"] != nil {
		t.Fatalf("other member must not see account: %v", got["payer_account_id"])
	}

	byExpense := func(u user) map[string]map[string]any {
		out := map[string]map[string]any{}
		for _, le := range list(e.must(200, "GET", "/api/v1/ledger?kind=GROUP_EXPENSE", u, nil), "data") {
			out[str(le["expense_id"])] = le
		}
		return out
	}
	al := byExpense(alice)
	if len(al) != 3 {
		t.Fatalf("alice ledger should have 3 group entries: %v", al)
	}
	if le := al[exp1ID]; num(le["amount"]) != 5000 || num(le["account_amount"]) != -10000 || str(le["account_id"]) != aBank ||
		le["read_only"] != true || str(le["group_id"]) != gid || str(le["category_id"]) != food || str(le["group_name"]) != "Flat" || num(le["total_amount"]) != 10000 {
		t.Fatalf("payer with account entry: %v", le)
	}
	if le := al[str(exp2["id"])]; num(le["amount"]) != 3000 || le["account_amount"] != nil || le["account_id"] != nil {
		t.Fatalf("non-payer share entry: %v", le)
	}
	if le := al[exp3ID]; num(le["amount"]) != 0 || num(le["account_amount"]) != -4000 {
		t.Fatalf("payer without share entry: %v", le)
	}
	bl := byExpense(bob)
	if len(bl) != 3 || num(bl[exp1ID]["amount"]) != 5000 || bl[exp1ID]["account_amount"] != nil || num(bl[exp3ID]["amount"]) != 4000 {
		t.Fatalf("bob ledger: %v", bl)
	}
	if got := list(e.must(200, "GET", "/api/v1/ledger", carol, nil), "data"); len(got) != 0 {
		t.Fatalf("carol sees group entries: %v", got)
	}

	// Balances and overview: full amounts leave the account, only shares are expenses.
	if b := num(e.accountsByName(alice)["Bank"]["balance"]); b != -14000 {
		t.Fatalf("alice bank: %d", b)
	}
	ov := e.must(200, "GET", "/api/v1/overview?from=2026-05-01&to=2026-05-31", alice, nil)
	if num(ov["expense"]) != 5000+3000 || num(ov["income"]) != 0 || num(ov["balance"]) != -14000 {
		t.Fatalf("alice overview: %v", ov)
	}
	if ov := e.must(200, "GET", "/api/v1/overview?from=2026-06-01", alice, nil); num(ov["expense"]) != 0 {
		t.Fatalf("range should exclude May: %v", ov)
	}

	// Editing the expense flows through; a new payer drops the old payer's link.
	upd := map[string]any{"description": "Groceries", "amount": 10000, "paid_by": bob.id.String(), "split_mode": "EQUAL", "category_id": food,
		"splits": []map[string]string{{"user_id": alice.id.String()}, {"user_id": bob.id.String()}}}
	e.must(200, "PATCH", "/api/v1/expenses/"+exp1ID, alice, upd)
	if b := num(e.accountsByName(alice)["Bank"]["balance"]); b != -4000 {
		t.Fatalf("alice bank after payer change: %d", b)
	}
	// Bob (now payer) links his account; omitting the field later keeps it.
	upd["payer_account_id"] = bWallet
	e.must(200, "PATCH", "/api/v1/expenses/"+exp1ID, bob, upd)
	delete(upd, "payer_account_id")
	upd["description"] = "Groceries 2"
	e.must(200, "PATCH", "/api/v1/expenses/"+exp1ID, alice, upd)
	if b := num(e.accountsByName(bob)["Wallet"]["balance"]); b != -10000 {
		t.Fatalf("bob wallet: %d", b)
	}
	upd["payer_account_id"] = nil
	e.status(403, "PATCH", "/api/v1/expenses/"+exp1ID, alice, upd)

	// Settlements: payer picks the source account on create, receiver links later.
	e.status(403, "POST", "/api/v1/groups/"+gid+"/settlements", alice, map[string]any{"from_user": bob.id.String(), "to_user": alice.id.String(), "amount": 2500, "from_account_id": aBank})
	e.status(422, "POST", "/api/v1/groups/"+gid+"/settlements", bob, map[string]any{"from_user": bob.id.String(), "to_user": alice.id.String(), "amount": 2500, "from_account_id": aBank})
	st := e.must(201, "POST", "/api/v1/groups/"+gid+"/settlements", bob, map[string]any{"from_user": bob.id.String(), "to_user": alice.id.String(), "amount": 2500, "from_account_id": bWallet})
	sid := str(st["id"])
	e.status(422, "PUT", "/api/v1/settlements/"+sid+"/account", alice, map[string]any{"account_id": bWallet})
	e.status(404, "PUT", "/api/v1/settlements/"+sid+"/account", carol, map[string]any{"account_id": nil})
	if got := e.must(200, "PUT", "/api/v1/settlements/"+sid+"/account", alice, map[string]any{"account_id": aBank}); str(got["account_id"]) != aBank {
		t.Fatalf("link receiver account: %v", got)
	}
	if b := num(e.accountsByName(alice)["Bank"]["balance"]); b != -4000+2500 {
		t.Fatalf("alice bank after settlement: %d", b)
	}
	if b := num(e.accountsByName(bob)["Wallet"]["balance"]); b != -10000-2500 {
		t.Fatalf("bob wallet after settlement: %d", b)
	}
	for _, sl := range list(e.must(200, "GET", "/api/v1/groups/"+gid+"/settlements", alice, nil), "data") {
		if str(sl["id"]) == sid && str(sl["account_id"]) != aBank {
			t.Fatalf("settlement list account for receiver: %v", sl)
		}
	}
	sEntries := list(e.must(200, "GET", "/api/v1/ledger?type=SETTLEMENT", alice, nil), "data")
	if len(sEntries) != 1 || num(sEntries[0]["account_amount"]) != 2500 || str(sEntries[0]["settlement_id"]) != sid || sEntries[0]["read_only"] != true {
		t.Fatalf("settlement ledger entry: %v", sEntries)
	}
	// Settlements move money but are neither income nor expense.
	if ov := e.must(200, "GET", "/api/v1/overview?from=2000-01-01", alice, nil); num(ov["income"]) != 0 || num(ov["expense"]) != 3000+5000 {
		t.Fatalf("overview after settlement: %v", ov)
	}
	e.must(200, "PUT", "/api/v1/settlements/"+sid+"/account", alice, map[string]any{"account_id": nil})
	if b := num(e.accountsByName(alice)["Bank"]["balance"]); b != -4000 {
		t.Fatalf("alice bank after unlinking: %d", b)
	}

	// Wallet transactions and group entries share the ledger; group filter works.
	e.must(201, "POST", "/api/v1/transactions", alice, map[string]any{"type": "EXPENSE", "account_id": aBank, "amount": 700, "category_id": food, "occurred_at": "2026-05-11T09:00:00Z"})
	all := list(e.must(200, "GET", "/api/v1/ledger", alice, nil), "data")
	if len(all) != 4 { // tx + exp1 share (bob paid) + exp2 + exp3
		t.Fatalf("alice full ledger: %d %v", len(all), all)
	}
	if got := list(e.must(200, "GET", "/api/v1/ledger?group="+gid, alice, nil), "data"); len(got) != 3 {
		t.Fatalf("group filter: %v", got)
	}
	if got := list(e.must(200, "GET", "/api/v1/ledger?account="+aBank, alice, nil), "data"); len(got) != 2 {
		t.Fatalf("account filter: %v", got)
	}

	// Category summary covers wallet transactions and group shares in the category.
	cs := e.must(200, "GET", "/api/v1/categories/"+food+"/summary?from=2026-05-01&to=2026-05-31", alice, nil)
	if num(cs["expense"]) != 5000+3000+700 || len(list(cs, "entries")) != 4 {
		t.Fatalf("category summary: %v", cs)
	}

	// Category management: own categories editable/deletable, system immutable.
	mine := e.must(201, "POST", "/api/v1/categories", alice, map[string]any{"name": "Pets"})
	mineID := str(mine["id"])
	txn := e.must(201, "POST", "/api/v1/transactions", alice, map[string]any{"type": "EXPENSE", "account_id": aBank, "amount": 100, "category_id": mineID})
	if got := e.must(200, "PATCH", "/api/v1/categories/"+mineID, alice, map[string]any{"name": "Cats", "color": "#000000"}); str(got["name"]) != "Cats" {
		t.Fatalf("patch category: %v", got)
	}
	e.status(403, "PATCH", "/api/v1/categories/"+food, alice, map[string]any{"name": "x"})
	e.status(403, "DELETE", "/api/v1/categories/"+food, alice, nil)
	e.status(404, "PATCH", "/api/v1/categories/"+mineID, bob, map[string]any{"name": "x"})
	e.status(404, "DELETE", "/api/v1/categories/"+mineID, bob, nil)
	e.status(404, "GET", "/api/v1/categories/"+mineID+"/summary", bob, nil)
	ordered := e.must(200, "POST", "/api/v1/categories/reorder", alice, map[string]any{"ids": []string{mineID, food}})
	if cats := list(ordered, "data"); str(cats[0]["id"]) != mineID || num(cats[1]["sort_order"]) != 1 {
		t.Fatalf("category reorder: %v", cats[:2])
	}
	e.status(422, "POST", "/api/v1/categories/reorder", bob, map[string]any{"ids": []string{mineID}})
	e.status(200, "DELETE", "/api/v1/categories/"+mineID, alice, nil)
	if got := e.must(200, "GET", "/api/v1/transactions/"+str(txn["id"]), alice, nil); got["category_id"] != nil {
		t.Fatalf("deleted category should be cleared on transactions: %v", got)
	}

	// Deleting an account unlinks it from group expenses.
	e.status(200, "DELETE", "/api/v1/accounts/"+aBank, alice, nil)
	if le := byExpense(alice)[exp3ID]; le != nil {
		t.Fatalf("payer-without-share entry should disappear with the account: %v", le)
	}
}
