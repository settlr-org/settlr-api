package wallet_test

import (
	"fmt"
	"net/url"
	"testing"

	"github.com/settlr-org/settlr-api/internal/testutil"
)

func TestIntegration_SearchWalletData(t *testing.T) {
	e := newEnv(t, testutil.CleanDB(t))
	alice := e.register("Alice", "alice-search-w@test.local")
	mallory := e.register("Mallory", "mallory-search-w@test.local")

	search := func(u user, q string) map[string]any {
		return e.must(200, "GET", "/api/v1/search?q="+url.QueryEscape(q), u, nil)
	}
	ids := func(res map[string]any, group string) map[string]bool {
		out := map[string]bool{}
		for _, r := range list(res, group) {
			out[str(r["id"])] = true
		}
		return out
	}

	bank := str(e.createAccount(alice, "Zebra Savings", "USD", 7000)["id"]) // initial balance = ADJUSTMENT row
	tag := e.must(201, "POST", "/api/v1/tags", alice, map[string]any{"name": "Quokka", "color": "#112233"})
	tx := func(body map[string]any) string {
		body["account_id"] = bank
		if _, ok := body["type"]; !ok {
			body["type"] = "EXPENSE"
		}
		return str(e.must(201, "POST", "/api/v1/transactions", alice, body)["id"])
	}
	byTitle := tx(map[string]any{"title": "Marmot lunch", "amount": 1250, "occurred_at": "2026-06-01"})
	byDesc := tx(map[string]any{"title": "x", "description": "bought a MARMOT plushie", "amount": 100, "occurred_at": "2026-06-02"})
	byTag := tx(map[string]any{"title": "Ferry", "amount": 300, "occurred_at": "2026-06-03", "tag_ids": []string{str(tag["id"])}})
	plannedTx := tx(map[string]any{"title": "Marmot insurance", "amount": 900, "due_at": "2026-12-01"})
	plan := e.must(201, "POST", "/api/v1/planned", alice, map[string]any{
		"type": "EXPENSE", "account_id": bank, "amount": 500, "title": "Marmot subscription", "start_at": "2026-11-01", "interval_n": 1, "interval_unit": "MONTH",
	})
	loan := e.must(201, "POST", "/api/v1/loans", alice, map[string]any{"name": "Marmot loan", "type": "LEND", "amount": 1000, "account_id": bank})
	e.must(201, "POST", "/api/v1/loans/"+str(loan["id"])+"/records", alice, map[string]any{"type": "DECREASE", "amount": 250})
	budget := e.must(201, "POST", "/api/v1/budgets", alice, map[string]any{"name": "Marmot budget", "amount": 5000})
	cat := e.must(201, "POST", "/api/v1/categories", alice, map[string]any{"name": "Marmot care"})

	// Mallory owns look-alike data of every kind.
	mBank := str(e.createAccount(mallory, "Marmot Vault", "USD", 100)["id"])
	e.must(201, "POST", "/api/v1/tags", mallory, map[string]any{"name": "Marmot tag"})
	e.must(201, "POST", "/api/v1/transactions", mallory, map[string]any{"type": "EXPENSE", "account_id": mBank, "title": "Marmot secret", "amount": 1, "occurred_at": "2026-06-01"})
	e.must(201, "POST", "/api/v1/planned", mallory, map[string]any{"type": "EXPENSE", "account_id": mBank, "amount": 1, "title": "Marmot secret plan", "start_at": "2026-11-01", "interval_n": 1, "interval_unit": "MONTH"})
	e.must(201, "POST", "/api/v1/loans", mallory, map[string]any{"name": "Marmot secret loan", "type": "BORROW", "amount": 1})
	e.must(201, "POST", "/api/v1/budgets", mallory, map[string]any{"name": "Marmot secret budget", "amount": 1})
	e.must(201, "POST", "/api/v1/categories", mallory, map[string]any{"name": "Marmot secret category"})

	res := search(alice, "marmot")
	// Existing keys are still there.
	for _, k := range []string{"users", "groups", "expenses"} {
		if _, ok := res[k]; !ok {
			t.Fatalf("missing legacy key %s", k)
		}
	}
	got := ids(res, "transactions")
	for _, want := range []string{byTitle, byDesc, plannedTx} {
		if !got[want] {
			t.Fatalf("transactions missing %s: %v", want, res["transactions"])
		}
	}
	// The plan's pending occurrence ("Marmot subscription") is a planned transaction too.
	if got[byTag] || len(got) != 4 {
		t.Fatalf("transactions: %v", res["transactions"])
	}
	for _, r := range list(res, "transactions") {
		if str(r["id"]) == plannedTx && (r["planned"] != true || r["occurred_at"] != nil || str(r["due_at"]) == "") {
			t.Fatalf("planned item shape: %v", r)
		}
		if str(r["id"]) == byTitle {
			if str(r["account_name"]) != "Zebra Savings" || str(r["account_id"]) != bank || str(r["currency"]) != "USD" ||
				num(r["amount"]) != 1250 || str(r["source"]) != "MANUAL" || r["planned"] != false {
				t.Fatalf("transaction shape: %v", r)
			}
			if _, ok := r["tags"].([]any); !ok {
				t.Fatalf("tags must be an array: %v", r)
			}
		}
	}
	if a := list(res, "accounts"); len(a) != 0 {
		t.Fatalf("accounts for marmot: %v", a)
	}
	if l := list(res, "loans"); len(l) != 1 || str(l[0]["id"]) != str(loan["id"]) || num(l[0]["remaining"]) != 750 || l[0]["paid_off"] != false || str(l[0]["type"]) != "LEND" || num(l[0]["amount"]) != 1000 {
		t.Fatalf("loans: %v", l)
	}
	if p := list(res, "planned"); len(p) != 1 || str(p[0]["id"]) != str(plan["id"]) || p[0]["active"] != true || p[0]["one_time"] != false ||
		num(p[0]["interval_n"]) != 1 || str(p[0]["interval_unit"]) != "MONTH" || str(p[0]["next_due_at"]) == "" {
		t.Fatalf("planned: %v", p)
	}
	if b := list(res, "budgets"); len(b) != 1 || str(b[0]["id"]) != str(budget["id"]) || num(b[0]["amount"]) != 5000 {
		t.Fatalf("budgets: %v", b)
	}
	if c := list(res, "categories"); len(c) != 1 || str(c[0]["id"]) != str(cat["id"]) {
		t.Fatalf("categories: %v", c)
	}
	if tg := list(res, "tags"); len(tg) != 0 {
		t.Fatalf("tags for marmot: %v", tg)
	}
	// Nothing of Mallory's anywhere in Alice's results.
	for _, group := range []string{"transactions", "accounts", "loans", "planned", "budgets", "categories", "tags"} {
		for _, r := range list(res, group) {
			if s := fmt.Sprint(r); contains(s, "secret") || contains(s, "Vault") {
				t.Fatalf("leak in %s: %v", group, r)
			}
		}
	}

	// Tag names match transactions; adjustments (initial balance) never appear.
	res = search(alice, "quokka")
	if g := ids(res, "transactions"); !g[byTag] || len(g) != 1 {
		t.Fatalf("tag search: %v", res["transactions"])
	}
	if tg := list(res, "tags"); len(tg) != 1 || str(tg[0]["name"]) != "Quokka" || str(tg[0]["color"]) != "#112233" {
		t.Fatalf("tags: %v", tg)
	}
	if tr := list(res, "transactions"); len(tr[0]["tags"].([]any)) != 1 {
		t.Fatalf("tx tags: %v", tr[0])
	}
	if a := list(search(alice, "zebra"), "accounts"); len(a) != 1 || str(a[0]["name"]) != "Zebra Savings" || str(a[0]["icon"]) == "" {
		t.Fatalf("accounts: %v", a)
	}
	if n := len(list(search(alice, "balance"), "transactions")); n != 0 {
		t.Fatalf("adjustment rows must not be searchable: %d", n)
	}
	// LOAN-source bookings are excluded (loans are listed separately).
	if n := len(list(search(alice, "marmot loan"), "transactions")); n != 0 {
		t.Fatalf("loan booking leaked into transactions: %d", n)
	}

	// Other users see only their own data.
	mres := search(mallory, "marmot")
	if len(list(mres, "transactions")) != 2 || len(list(mres, "loans")) != 1 || len(list(mres, "planned")) != 1 ||
		len(list(mres, "budgets")) != 1 || len(list(mres, "accounts")) != 1 || len(list(mres, "tags")) != 1 {
		t.Fatalf("mallory results: %v", mres)
	}
	for _, group := range []string{"transactions", "loans", "planned", "budgets", "categories"} {
		for _, r := range list(mres, group) {
			if contains(fmt.Sprint(r), "Marmot lunch") || contains(fmt.Sprint(r), "Marmot loan") {
				t.Fatalf("alice data in mallory results: %v", r)
			}
		}
	}
	// Categories include system ones.
	sys := e.systemCategory("Marmot system")
	if c := ids(search(alice, "marmot system"), "categories"); !c[sys] {
		t.Fatalf("system category not found")
	}
	if c := ids(search(mallory, "marmot care"), "categories"); len(c) != 0 {
		t.Fatalf("foreign custom category visible: %v", c)
	}

	// Auth required.
	if s, _ := e.do("GET", "/api/v1/search?q=marmot", "", nil); s != 401 {
		t.Fatalf("unauthenticated search: %d", s)
	}
	// Empty query: legacy keys plus empty wallet groups.
	res = search(alice, "")
	for _, k := range []string{"users", "groups", "expenses", "transactions", "accounts", "loans", "planned", "budgets", "categories", "tags"} {
		if a, ok := res[k].([]any); !ok || len(a) != 0 {
			t.Fatalf("empty q %s: %v", k, res[k])
		}
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func TestIntegration_SearchEscapingAndCaps(t *testing.T) {
	e := newEnv(t, testutil.CleanDB(t))
	u := e.register("Esc", "esc-search-w@test.local")
	bank := str(e.createAccount(u, "Main", "USD", 0)["id"])
	mk := func(title string, i int) {
		e.must(201, "POST", "/api/v1/transactions", u, map[string]any{
			"type": "EXPENSE", "account_id": bank, "title": title, "amount": 100 + i, "occurred_at": fmt.Sprintf("2026-06-%02dT10:00:00Z", 1+i%28),
		})
	}
	mk("100% done", 0)
	mk("100 percent", 1)
	mk("snake_case", 2)
	mk("snakeXcase", 3)
	mk(`back\slash`, 4)
	count := func(q string) int {
		return len(list(e.must(200, "GET", "/api/v1/search?q="+url.QueryEscape(q), u, nil), "transactions"))
	}
	if n := count("100%"); n != 1 {
		t.Fatalf("%% must match literally: %d", n)
	}
	if n := count("%"); n != 1 {
		t.Fatalf("lone %%: %d", n)
	}
	if n := count("snake_case"); n != 1 {
		t.Fatalf("_ must match literally: %d", n)
	}
	if n := count("_"); n != 1 {
		t.Fatalf("lone _: %d", n)
	}
	if n := count(`k\s`); n != 1 {
		t.Fatalf("backslash literal: %d", n)
	}
	if n := count("SNAKE"); n != 2 {
		t.Fatalf("case-insensitive: %d", n)
	}
	if n := count(" snake "); n != 0 && n != 2 { // trimmed like today's search
		t.Fatalf("trim: %d", n)
	}

	// Caps: 10 per group, newest first.
	for i := 0; i < 14; i++ {
		mk(fmt.Sprintf("Capped item %02d", i), i)
		e.must(201, "POST", "/api/v1/accounts", u, map[string]any{"name": fmt.Sprintf("Capped acct %02d", i), "currency": "USD"})
		e.must(201, "POST", "/api/v1/tags", u, map[string]any{"name": fmt.Sprintf("Capped tag %02d", i)})
		e.must(201, "POST", "/api/v1/budgets", u, map[string]any{"name": fmt.Sprintf("Capped budget %02d", i), "amount": 10})
		e.must(201, "POST", "/api/v1/loans", u, map[string]any{"name": fmt.Sprintf("Capped loan %02d", i), "type": "LEND", "amount": 10})
		e.must(201, "POST", "/api/v1/categories", u, map[string]any{"name": fmt.Sprintf("Capped cat %02d", i)})
		e.must(201, "POST", "/api/v1/planned", u, map[string]any{"type": "EXPENSE", "account_id": bank, "amount": 5, "title": fmt.Sprintf("Capped plan %02d", i), "start_at": "2026-12-01", "interval_n": 1, "interval_unit": "MONTH"})
	}
	res := e.must(200, "GET", "/api/v1/search?q=capped", u, nil)
	for _, g := range []string{"transactions", "accounts", "loans", "planned", "budgets", "categories", "tags"} {
		if n := len(list(res, g)); n != 10 {
			t.Fatalf("%s capped at %d, want 10", g, n)
		}
	}
}
