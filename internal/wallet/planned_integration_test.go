package wallet_test

import (
	"net/url"
	"testing"
	"time"

	"github.com/settlr-org/settlr-api/internal/testutil"
)

func dueOf(t map[string]any) string {
	if t == nil {
		return ""
	}
	return str(t["due_at"])
}

func asMap(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

func TestIntegration_PlannedRecurrencePaySkip(t *testing.T) {
	e := newEnv(t, testutil.CleanDB(t))
	alice := e.register("Alice", "alice-wallet-plan@test.local")
	bank := str(e.createAccount(alice, "Bank", "NPR", 100000)["id"])
	balance := func() int64 { return num(e.accountsByName(alice)["Bank"]["balance"]) }

	// Monthly from Jan 31: month-end clamp, anchored to the start.
	plan := e.must(201, "POST", "/api/v1/planned", alice, map[string]any{
		"type": "EXPENSE", "account_id": bank, "amount": 1000, "title": "Rent",
		"start_at": "2021-01-31", "interval_n": 1, "interval_unit": "MONTH",
	})
	pid := str(plan["id"])
	next := asMap(plan["next"])
	if dueOf(next) != "2021-01-31T00:00:00Z" || plan["active"] != true || next["planned"] != true || str(next["planned_payment_id"]) != pid {
		t.Fatalf("created plan: %v", plan)
	}
	if b := balance(); b != 100000 {
		t.Fatalf("pending must not move the balance: %d", b)
	}
	if got := list(e.must(200, "GET", "/api/v1/transactions?planned=true", alice, nil), "data"); len(got) != 1 {
		t.Fatalf("one pending occurrence: %v", got)
	}
	if got := list(e.must(200, "GET", "/api/v1/ledger", alice, nil), "data"); len(got) != 1 { // only the initial balance
		t.Fatalf("ledger excludes planned by default: %v", got)
	}
	if got := list(e.must(200, "GET", "/api/v1/ledger?planned=all", alice, nil), "data"); len(got) != 2 {
		t.Fatalf("ledger planned=all: %v", got)
	}

	// Pay with no body: occurred now, next occurrence Feb 28.
	res := e.must(200, "POST", "/api/v1/transactions/"+str(next["id"])+"/pay", alice, nil)
	paid := asMap(res["transaction"])
	if paid["occurred_at"] == nil || paid["planned"] != false || dueOf(paid) != "2021-01-31T00:00:00Z" {
		t.Fatalf("paid: %v", res)
	}
	if dueOf(asMap(res["next"])) != "2021-02-28T00:00:00Z" {
		t.Fatalf("Jan 31 + 1 month: %v", res["next"])
	}
	if b := balance(); b != 99000 {
		t.Fatalf("paid occurrence counts: %d", b)
	}
	e.status(409, "POST", "/api/v1/transactions/"+str(paid["id"])+"/pay", alice, nil)
	e.status(409, "POST", "/api/v1/transactions/"+str(paid["id"])+"/skip", alice, nil)

	// Pay with overrides; then Mar 31 (back to the anchor day).
	res = e.must(200, "POST", "/api/v1/transactions/"+str(asMap(res["next"])["id"])+"/pay", alice, map[string]any{"amount": 500, "occurred_at": "2021-03-01"})
	if p := asMap(res["transaction"]); num(p["amount"]) != 500 || str(p["occurred_at"]) != "2021-03-01T00:00:00Z" {
		t.Fatalf("pay overrides: %v", p)
	}
	if dueOf(asMap(res["next"])) != "2021-03-31T00:00:00Z" {
		t.Fatalf("Feb 28 -> Mar 31: %v", res["next"])
	}
	// Skip: the occurrence disappears, Apr 30 is next.
	skipped := str(asMap(res["next"])["id"])
	res = e.must(200, "POST", "/api/v1/transactions/"+skipped+"/skip", alice, nil)
	if dueOf(asMap(res["next"])) != "2021-04-30T00:00:00Z" {
		t.Fatalf("skip -> Apr 30: %v", res)
	}
	e.status(404, "GET", "/api/v1/transactions/"+skipped, alice, nil)
	if b := balance(); b != 98500 {
		t.Fatalf("balance after pay/pay/skip: %d", b)
	}
	// Deleting the pending occurrence through /transactions behaves like skip.
	e.must(200, "DELETE", "/api/v1/transactions/"+str(asMap(res["next"])["id"]), alice, nil)
	if got := e.must(200, "GET", "/api/v1/planned/"+pid, alice, nil); dueOf(asMap(got["next"])) != "2021-05-31T00:00:00Z" {
		t.Fatalf("delete pending acts as skip: %v", got["next"])
	}
	// PATCH occurred_at on the pending occurrence pays it and advances the plan.
	cur := asMap(e.must(200, "GET", "/api/v1/planned/"+pid, alice, nil)["next"])
	e.must(200, "PATCH", "/api/v1/transactions/"+str(cur["id"]), alice, map[string]any{"occurred_at": "2021-06-01"})
	if got := e.must(200, "GET", "/api/v1/planned/"+pid, alice, nil); dueOf(asMap(got["next"])) != "2021-06-30T00:00:00Z" {
		t.Fatalf("patch occurred_at pays: %v", got["next"])
	}
	// A paid occurrence cannot become planned again.
	e.status(422, "PATCH", "/api/v1/transactions/"+str(paid["id"]), alice, map[string]any{"occurred_at": nil})

	// Weekly and daily.
	weekly := e.must(201, "POST", "/api/v1/planned", alice, map[string]any{
		"type": "INCOME", "account_id": bank, "amount": 50, "start_at": "2021-01-04", "interval_unit": "WEEK", "interval_n": 2,
	})
	if num(weekly["interval_n"]) != 2 {
		t.Fatalf("weekly: %v", weekly)
	}
	res = e.must(200, "POST", "/api/v1/transactions/"+str(asMap(weekly["next"])["id"])+"/pay", alice, nil)
	if dueOf(asMap(res["next"])) != "2021-01-18T00:00:00Z" {
		t.Fatalf("every 2 weeks: %v", res["next"])
	}
	daily := e.must(201, "POST", "/api/v1/planned", alice, map[string]any{
		"type": "EXPENSE", "account_id": bank, "amount": 5, "start_at": "2021-01-30T08:00:00Z", "interval_unit": "DAY", "interval_n": 3,
	})
	res = e.must(200, "POST", "/api/v1/transactions/"+str(asMap(daily["next"])["id"])+"/skip", alice, nil)
	if dueOf(asMap(res["next"])) != "2021-02-02T08:00:00Z" {
		t.Fatalf("every 3 days: %v", res["next"])
	}

	// Yearly from a leap day: Feb 28 in common years, Feb 29 again in 2028.
	yearly := e.must(201, "POST", "/api/v1/planned", alice, map[string]any{
		"type": "EXPENSE", "account_id": bank, "amount": 7, "start_at": "2024-02-29", "interval_unit": "YEAR",
	})
	want := []string{"2025-02-28", "2026-02-28", "2027-02-28", "2028-02-29"}
	id := str(asMap(yearly["next"])["id"])
	for _, w := range want {
		res = e.must(200, "POST", "/api/v1/transactions/"+id+"/skip", alice, nil)
		if dueOf(asMap(res["next"])) != w+"T00:00:00Z" {
			t.Fatalf("leap-day yearly: want %s, got %v", w, res["next"])
		}
		id = str(asMap(res["next"])["id"])
	}

	// Validation.
	e.status(422, "POST", "/api/v1/planned", alice, map[string]any{"type": "EXPENSE", "account_id": bank, "amount": 1, "start_at": "2021-01-01"})
	e.status(422, "POST", "/api/v1/planned", alice, map[string]any{"type": "EXPENSE", "account_id": bank, "amount": 1, "start_at": "2021-01-01", "interval_unit": "HOUR"})
	e.status(422, "POST", "/api/v1/planned", alice, map[string]any{"type": "EXPENSE", "account_id": bank, "amount": 1, "interval_unit": "DAY"})
}

func TestIntegration_PlannedOneTimeEditDelete(t *testing.T) {
	e := newEnv(t, testutil.CleanDB(t))
	alice := e.register("Alice", "alice-wallet-plan2@test.local")
	bob := e.register("Bob", "bob-wallet-plan2@test.local")
	bank := str(e.createAccount(alice, "Bank", "NPR", 0)["id"])
	bobBank := str(e.createAccount(bob, "Bank", "NPR", 0)["id"])
	food := e.systemCategory("Food")
	tag := str(e.must(201, "POST", "/api/v1/tags", alice, map[string]any{"name": "bills"})["id"])

	// One-time: paying completes the plan.
	once := e.must(201, "POST", "/api/v1/planned", alice, map[string]any{
		"type": "EXPENSE", "account_id": bank, "amount": 300, "start_at": "2030-05-01", "one_time": true, "tag_ids": []string{tag},
	})
	if once["interval_unit"] != nil || len(asMap(once["next"])["tags"].([]any)) != 1 || len(once["tags"].([]any)) != 1 {
		t.Fatalf("one-time plan: %v", once)
	}
	res := e.must(200, "POST", "/api/v1/transactions/"+str(asMap(once["next"])["id"])+"/pay", alice, nil)
	if res["next"] != nil {
		t.Fatalf("one-time has no next: %v", res)
	}
	if got := e.must(200, "GET", "/api/v1/planned/"+str(once["id"]), alice, nil); got["active"] != false || got["next"] != nil {
		t.Fatalf("one-time completed: %v", got)
	}
	// Skipping a one-time plan also completes it.
	once2 := e.must(201, "POST", "/api/v1/planned", alice, map[string]any{
		"type": "INCOME", "account_id": bank, "amount": 300, "start_at": "2030-05-01", "one_time": true,
	})
	e.must(200, "POST", "/api/v1/transactions/"+str(asMap(once2["next"])["id"])+"/skip", alice, nil)
	if got := e.must(200, "GET", "/api/v1/planned/"+str(once2["id"]), alice, nil); got["active"] != false || got["next"] != nil {
		t.Fatalf("one-time skipped: %v", got)
	}
	// Re-activating re-opens it at start_at.
	if got := e.must(200, "PATCH", "/api/v1/planned/"+str(once2["id"]), alice, map[string]any{"active": true}); dueOf(asMap(got["next"])) != "2030-05-01T00:00:00Z" {
		t.Fatalf("re-opened: %v", got)
	}

	// Edits regenerate the pending occurrence in place.
	plan := e.must(201, "POST", "/api/v1/planned", alice, map[string]any{
		"type": "EXPENSE", "account_id": bank, "amount": 1000, "start_at": "2021-01-15", "interval_unit": "MONTH", "category_id": food,
	})
	pid := str(plan["id"])
	pendingID := str(asMap(plan["next"])["id"])
	got := e.must(200, "PATCH", "/api/v1/planned/"+pid, alice, map[string]any{"amount": 2000, "title": "Gym", "tag_ids": []string{tag}})
	n := asMap(got["next"])
	if str(n["id"]) != pendingID || num(n["amount"]) != 2000 || str(n["title"]) != "Gym" || len(n["tags"].([]any)) != 1 {
		t.Fatalf("amount edit regenerates in place: %v", got)
	}
	got = e.must(200, "PATCH", "/api/v1/planned/"+pid, alice, map[string]any{"start_at": "2021-01-20"})
	if dueOf(asMap(got["next"])) != "2021-01-20T00:00:00Z" {
		t.Fatalf("start_at edit: %v", got["next"])
	}
	res = e.must(200, "POST", "/api/v1/transactions/"+pendingID+"/pay", alice, map[string]any{"occurred_at": "2021-01-20"})
	paidID := str(asMap(res["transaction"])["id"])
	if dueOf(asMap(res["next"])) != "2021-02-20T00:00:00Z" {
		t.Fatalf("after pay: %v", res["next"])
	}
	got = e.must(200, "PATCH", "/api/v1/planned/"+pid, alice, map[string]any{"interval_unit": "WEEK"})
	if dueOf(asMap(got["next"])) != "2021-01-27T00:00:00Z" {
		t.Fatalf("schedule edit continues after the last resolved occurrence: %v", got["next"])
	}
	got = e.must(200, "PATCH", "/api/v1/planned/"+pid, alice, map[string]any{"active": false})
	if got["next"] != nil {
		t.Fatalf("inactive has no pending: %v", got)
	}
	if got := list(e.must(200, "GET", "/api/v1/planned?active=false", alice, nil), "data"); len(got) != 2 {
		t.Fatalf("inactive filter: %d", len(got))
	}
	got = e.must(200, "PATCH", "/api/v1/planned/"+pid, alice, map[string]any{"active": true})
	if dueOf(asMap(got["next"])) != "2021-01-27T00:00:00Z" {
		t.Fatalf("re-activated: %v", got["next"])
	}

	// Ownership: other users' plans are 404, foreign ids in the body 422.
	e.status(404, "GET", "/api/v1/planned/"+pid, bob, nil)
	e.status(404, "PATCH", "/api/v1/planned/"+pid, bob, map[string]any{"amount": 1})
	e.status(404, "DELETE", "/api/v1/planned/"+pid, bob, nil)
	e.status(404, "POST", "/api/v1/transactions/"+str(asMap(got["next"])["id"])+"/pay", bob, nil)
	e.status(404, "POST", "/api/v1/transactions/"+str(asMap(got["next"])["id"])+"/skip", bob, nil)
	e.status(422, "POST", "/api/v1/planned", alice, map[string]any{"type": "EXPENSE", "account_id": bobBank, "amount": 1, "start_at": "2021-01-01", "one_time": true})
	e.status(422, "PATCH", "/api/v1/planned/"+pid, alice, map[string]any{"account_id": bobBank})
	e.status(422, "POST", "/api/v1/transactions/"+str(asMap(got["next"])["id"])+"/pay", alice, map[string]any{"account_id": bobBank})
	if got := list(e.must(200, "GET", "/api/v1/planned", bob, nil), "data"); len(got) != 0 {
		t.Fatalf("bob sees no plans: %v", got)
	}

	// Delete: pending gone, paid history kept.
	pending := str(asMap(got["next"])["id"])
	e.must(200, "DELETE", "/api/v1/planned/"+pid, alice, nil)
	e.status(404, "GET", "/api/v1/planned/"+pid, alice, nil)
	e.status(404, "GET", "/api/v1/transactions/"+pending, alice, nil)
	if h := e.must(200, "GET", "/api/v1/transactions/"+paidID, alice, nil); str(h["planned_payment_id"]) != pid {
		t.Fatalf("paid history keeps the plan link: %v", h)
	}

	// Standalone planned items (no plan, no account) can be paid and skipped.
	free := e.must(201, "POST", "/api/v1/transactions", alice, map[string]any{"type": "EXPENSE", "amount": 70, "currency": "NPR", "due_at": "2030-01-01"})
	e.status(422, "POST", "/api/v1/transactions/"+str(free["id"])+"/pay", alice, nil)
	res = e.must(200, "POST", "/api/v1/transactions/"+str(free["id"])+"/pay", alice, map[string]any{"account_id": bank})
	if p := asMap(res["transaction"]); str(p["account_id"]) != bank || p["planned"] != false || res["next"] != nil {
		t.Fatalf("standalone pay: %v", res)
	}
	free2 := e.must(201, "POST", "/api/v1/transactions", alice, map[string]any{"type": "EXPENSE", "account_id": bank, "amount": 70, "due_at": "2030-01-01"})
	e.must(200, "POST", "/api/v1/transactions/"+str(free2["id"])+"/skip", alice, nil)
	e.status(404, "GET", "/api/v1/transactions/"+str(free2["id"]), alice, nil)

	// Idempotent writes.
	hdr := map[string]string{"Idempotency-Key": "plan-create-1"}
	body := map[string]any{"type": "EXPENSE", "account_id": bank, "amount": 123, "start_at": "2030-01-01", "one_time": true}
	s1, b1 := e.raw("POST", "/api/v1/planned", alice.token, body, hdr)
	s2, b2 := e.raw("POST", "/api/v1/planned", alice.token, body, hdr)
	if s1 != 201 || s2 != 201 || string(b1) != string(b2) {
		t.Fatalf("idempotent plan create: %d %d", s1, s2)
	}
	var created map[string]any
	for _, p := range list(e.must(200, "GET", "/api/v1/planned", alice, nil), "data") {
		if num(p["amount"]) == 123 {
			if created != nil {
				t.Fatal("idempotent create duplicated the plan")
			}
			created = p
		}
	}
	payHdr := map[string]string{"Idempotency-Key": "plan-pay-1"}
	path := "/api/v1/transactions/" + str(asMap(created["next"])["id"]) + "/pay"
	s1, b1 = e.raw("POST", path, alice.token, nil, payHdr)
	s2, b2 = e.raw("POST", path, alice.token, nil, payHdr)
	if s1 != 200 || s2 != 200 || string(b1) != string(b2) {
		t.Fatalf("idempotent pay replays: %d %s / %d %s", s1, b1, s2, b2)
	}
}

func TestIntegration_PlannedOverviewSkipOverdue(t *testing.T) {
	e := newEnv(t, testutil.CleanDB(t))
	alice := e.register("Alice", "alice-wallet-plan3@test.local")
	bank := str(e.createAccount(alice, "Bank", "NPR", 100000)["id"])
	hidden := e.must(201, "POST", "/api/v1/accounts", alice, map[string]any{"name": "Hidden", "currency": "NPR", "include_in_balance": false})
	food := e.systemCategory("Food")
	now := time.Now().UTC()
	at := func(d time.Duration) string { return now.Add(d).Format(time.RFC3339) }
	const day = 24 * time.Hour

	overdue := e.must(201, "POST", "/api/v1/planned", alice, map[string]any{
		"type": "EXPENSE", "account_id": bank, "amount": 1000, "start_at": at(-2 * day), "interval_unit": "MONTH", "category_id": food, "title": "Rent",
	})
	e.must(201, "POST", "/api/v1/transactions", alice, map[string]any{"type": "INCOME", "account_id": bank, "amount": 5000, "due_at": at(2 * day), "title": "Salary"})
	e.must(201, "POST", "/api/v1/transactions", alice, map[string]any{"type": "EXPENSE", "account_id": str(hidden["id"]), "amount": 300, "due_at": at(3 * day), "title": "Hidden"})
	e.must(201, "POST", "/api/v1/transactions", alice, map[string]any{"type": "EXPENSE", "account_id": bank, "amount": 700, "due_at": at(40 * day), "title": "Later"})
	e.must(201, "POST", "/api/v1/transactions", alice, map[string]any{"type": "EXPENSE", "amount": 200, "currency": "USD", "due_at": at(-5 * day), "title": "Old USD"})

	q := "?from=" + url.QueryEscape(at(-day)) + "&to=" + url.QueryEscape(at(10*day))
	ov := e.must(200, "GET", "/api/v1/overview"+q, alice, nil)
	up, od := list(ov, "upcoming"), list(ov, "overdue")
	if len(up) != 2 || str(up[0]["title"]) != "Salary" || str(up[1]["title"]) != "Hidden" {
		t.Fatalf("upcoming (in period, sorted by due): %v", up)
	}
	if len(od) != 2 || str(od[0]["title"]) != "Old USD" || str(od[1]["title"]) != "Rent" {
		t.Fatalf("overdue (any date before now, sorted): %v", od)
	}
	if p := asMap(od[1]["plan"]); str(p["id"]) != str(overdue["id"]) || str(p["interval_unit"]) != "MONTH" || od[0]["plan"] != nil {
		t.Fatalf("plan info: %v / %v", od[1]["plan"], od[0]["plan"])
	}
	// 200 USD cents = 266.00 NPR = 26600 minor at 133.
	if num(ov["balance"]) != 100000 || num(ov["upcoming_income"]) != 5000 || num(ov["upcoming_expense"]) != 300 ||
		num(ov["overdue_expense"]) != 27600 || num(ov["overdue_income"]) != 0 {
		t.Fatalf("overview sums: %v", ov)
	}
	// Projected: balance + salary − rent − old USD (hidden account and later item excluded).
	if got := num(ov["projected_balance"]); got != 100000+5000-1000-26600 {
		t.Fatalf("projected_balance: %d (%v)", got, ov)
	}
	if num(ov["income"]) != 0 || num(ov["expense"]) != 0 {
		t.Fatalf("planned items are not flows: %v", ov)
	}

	sum := e.must(200, "GET", "/api/v1/accounts/"+bank+"/summary"+q, alice, nil)
	if len(list(sum, "upcoming")) != 1 || len(list(sum, "overdue")) != 1 || num(sum["balance"]) != 100000 {
		t.Fatalf("account summary planned: %v", sum)
	}
	cat := e.must(200, "GET", "/api/v1/categories/"+food+"/summary"+q, alice, nil)
	if len(list(cat, "upcoming")) != 0 || len(list(cat, "overdue")) != 1 {
		t.Fatalf("category summary planned: %v", cat)
	}

	// A daily plan far behind plus a one-time overdue plan.
	e.must(201, "POST", "/api/v1/planned", alice, map[string]any{
		"type": "EXPENSE", "account_id": bank, "amount": 10, "start_at": at(-10*day - time.Hour), "interval_unit": "DAY", "title": "Daily",
	})
	once := e.must(201, "POST", "/api/v1/planned", alice, map[string]any{
		"type": "EXPENSE", "account_id": bank, "amount": 10, "start_at": at(-day), "one_time": true, "title": "Once",
	})
	res := e.must(200, "POST", "/api/v1/transactions/skip-overdue", alice, nil)
	if num(res["skipped"]) != 4 {
		t.Fatalf("skip-overdue: %v", res)
	}
	ov = e.must(200, "GET", "/api/v1/overview"+q, alice, nil)
	if got := list(ov, "overdue"); len(got) != 0 {
		t.Fatalf("nothing overdue after skip-overdue: %v", got)
	}
	plans := map[string]map[string]any{}
	for _, p := range list(e.must(200, "GET", "/api/v1/planned", alice, nil), "data") {
		plans[str(p["title"])] = p
	}
	d, _ := time.Parse(time.RFC3339, dueOf(asMap(plans["Daily"]["next"])))
	if d.Before(now) || d.After(now.Add(day)) {
		t.Fatalf("daily plan moved to the first occurrence from now: %v", d)
	}
	if plans["Once"]["active"] != false || plans["Once"]["next"] != nil || str(plans["Once"]["id"]) != str(once["id"]) {
		t.Fatalf("one-time overdue plan completed: %v", plans["Once"])
	}
	if r, _ := time.Parse(time.RFC3339, dueOf(asMap(plans["Rent"]["next"]))); !r.After(now) {
		t.Fatalf("monthly rent moved on: %v", r)
	}
	if b := num(e.accountsByName(alice)["Bank"]["balance"]); b != 100000 {
		t.Fatalf("skipping never moves balances: %d", b)
	}
}
