package wallet_test

import (
	"net/url"
	"testing"

	"github.com/settlr-org/settlr-api/internal/testutil"
)

func TestIntegration_TransactionsTagsFiltersAndPagination(t *testing.T) {
	e := newEnv(t, testutil.CleanDB(t))
	alice := e.register("Alice", "alice-wallet-tx@test.local")
	mallory := e.register("Mallory", "mallory-wallet-tx@test.local")
	food := e.systemCategory("Food")

	bank := str(e.createAccount(alice, "Bank", "NPR", 0)["id"])
	malloryAcc := str(e.createAccount(mallory, "M", "NPR", 0)["id"])

	trip := e.must(201, "POST", "/api/v1/tags", alice, map[string]any{"name": "Trip", "color": "#ff0000"})
	work := e.must(201, "POST", "/api/v1/tags", alice, map[string]any{"name": "Work"})
	e.status(409, "POST", "/api/v1/tags", alice, map[string]any{"name": "trip"})
	e.status(422, "POST", "/api/v1/tags", alice, map[string]any{"name": " "})
	malloryTag := e.must(201, "POST", "/api/v1/tags", mallory, map[string]any{"name": "Mine"})
	malloryCat := e.must(201, "POST", "/api/v1/categories", mallory, map[string]any{"name": "Secret"})
	tripID, workID := str(trip["id"]), str(work["id"])

	// Ownership validation on referenced resources.
	e.status(422, "POST", "/api/v1/transactions", alice, map[string]any{"type": "EXPENSE", "account_id": bank, "amount": 10, "tag_ids": []string{str(malloryTag["id"])}})
	e.status(422, "POST", "/api/v1/transactions", alice, map[string]any{"type": "EXPENSE", "account_id": bank, "amount": 10, "category_id": str(malloryCat["id"])})
	e.status(422, "POST", "/api/v1/transactions", alice, map[string]any{"type": "EXPENSE", "account_id": malloryAcc, "amount": 10})
	e.status(422, "POST", "/api/v1/transactions", alice, map[string]any{"type": "EXPENSE", "account_id": bank, "amount": 10, "occurred_at": "soon"})

	lunch := e.must(201, "POST", "/api/v1/transactions", alice, map[string]any{
		"type": "EXPENSE", "account_id": bank, "amount": 1500, "category_id": food, "title": "Lunch",
		"description": "noodles", "occurred_at": "2026-03-01T12:00:00Z", "tag_ids": []string{tripID, workID},
	})
	if len(list(lunch, "tags")) != 2 || str(lunch["category_id"]) != food || lunch["planned"] != false {
		t.Fatalf("lunch: %v", lunch)
	}
	e.must(201, "POST", "/api/v1/transactions", alice, map[string]any{"type": "INCOME", "account_id": bank, "amount": 50000, "title": "Salary", "occurred_at": "2026-03-02"})
	e.must(201, "POST", "/api/v1/transactions", alice, map[string]any{"type": "EXPENSE", "account_id": bank, "amount": 300, "title": "Coffee", "occurred_at": "2026-03-03T08:00:00+05:45", "tag_ids": []string{workID}})
	// Planned item without an account carries its own currency and is hidden by default.
	planned := e.must(201, "POST", "/api/v1/transactions", alice, map[string]any{"type": "EXPENSE", "amount": 999, "currency": "USD", "title": "Rent", "due_at": "2026-04-01"})
	if planned["planned"] != true || planned["occurred_at"] != nil || str(planned["currency"]) != "USD" {
		t.Fatalf("planned: %v", planned)
	}

	q := func(params string) []map[string]any {
		return list(e.must(200, "GET", "/api/v1/transactions?"+params, alice, nil), "data")
	}
	if got := q(""); len(got) != 3 || str(got[0]["title"]) != "Coffee" {
		t.Fatalf("default list (newest first, no planned): %v", got)
	}
	if got := q("planned=true"); len(got) != 1 || str(got[0]["title"]) != "Rent" {
		t.Fatalf("planned list: %v", got)
	}
	if got := q("tag=" + tripID); len(got) != 1 || str(got[0]["title"]) != "Lunch" {
		t.Fatalf("tag filter: %v", got)
	}
	if got := q("excl_tag=" + workID); len(got) != 1 || str(got[0]["title"]) != "Salary" {
		t.Fatalf("excl_tag filter: %v", got)
	}
	if got := q("type=EXPENSE&min=1000"); len(got) != 1 || str(got[0]["title"]) != "Lunch" {
		t.Fatalf("type+min filter: %v", got)
	}
	if got := q("q=NOODLE"); len(got) != 1 {
		t.Fatalf("q filter: %v", got)
	}
	if got := q("excl_q=coffee&max=20000"); len(got) != 1 || str(got[0]["title"]) != "Lunch" {
		t.Fatalf("excl_q+max filter: %v", got)
	}
	if got := q("category=" + food + "&account=" + bank); len(got) != 1 {
		t.Fatalf("category filter: %v", got)
	}
	// Date-only bounds are whole UTC days; Coffee (08:00+05:45 on the 3rd) is 02:15Z on the 3rd.
	if got := q("from=2026-03-02&to=2026-03-02"); len(got) != 1 || str(got[0]["title"]) != "Salary" {
		t.Fatalf("date range: %v", got)
	}
	e.status(422, "GET", "/api/v1/transactions?account=nope", alice, nil)
	e.status(422, "GET", "/api/v1/transactions?cursor=not-a-cursor!", alice, nil)

	// Cursor pagination.
	page1 := e.must(200, "GET", "/api/v1/transactions?limit=2", alice, nil)
	cursor := str(page1["next_cursor"])
	if len(list(page1, "data")) != 2 || cursor == "" {
		t.Fatalf("page1: %v", page1)
	}
	page2 := e.must(200, "GET", "/api/v1/transactions?limit=2&cursor="+url.QueryEscape(cursor), alice, nil)
	if p2 := list(page2, "data"); len(p2) != 1 || str(p2[0]["title"]) != "Lunch" || page2["next_cursor"] != nil {
		t.Fatalf("page2: %v", page2)
	}

	// PATCH: clear tags and category, change amount; omitted fields keep values.
	lunchID := str(lunch["id"])
	p := e.must(200, "PATCH", "/api/v1/transactions/"+lunchID, alice, map[string]any{"tag_ids": []string{}, "category_id": nil, "amount": 1600})
	if len(list(p, "tags")) != 0 || p["category_id"] != nil || num(p["amount"]) != 1600 || str(p["title"]) != "Lunch" {
		t.Fatalf("patch: %v", p)
	}
	e.status(422, "PATCH", "/api/v1/transactions/"+lunchID, alice, map[string]any{"type": "TRANSFER"})
	p = e.must(200, "PATCH", "/api/v1/transactions/"+lunchID, alice, map[string]any{"tag_ids": []string{tripID}})
	if len(list(p, "tags")) != 1 {
		t.Fatalf("re-tag: %v", p)
	}

	// Duplicate keeps fields and tags under a new id.
	dup := e.must(201, "POST", "/api/v1/transactions/"+lunchID+"/duplicate", alice, nil)
	if str(dup["id"]) == lunchID || num(dup["amount"]) != 1600 || len(list(dup, "tags")) != 1 || str(dup["source"]) != "MANUAL" {
		t.Fatalf("duplicate: %v", dup)
	}

	// Tag CRUD: patch/delete detach; other users get 404.
	e.must(200, "PATCH", "/api/v1/tags/"+tripID, alice, map[string]any{"name": "Holiday"})
	if got := e.must(200, "GET", "/api/v1/transactions/"+lunchID, alice, nil); str(list(got, "tags")[0]["name"]) != "Holiday" {
		t.Fatalf("renamed tag: %v", got)
	}
	e.status(404, "PATCH", "/api/v1/tags/"+tripID, mallory, map[string]any{"name": "x"})
	e.status(404, "DELETE", "/api/v1/tags/"+tripID, mallory, nil)
	e.status(404, "GET", "/api/v1/tags/"+tripID, mallory, nil)
	e.status(200, "DELETE", "/api/v1/tags/"+tripID, alice, nil)
	if got := e.must(200, "GET", "/api/v1/transactions/"+lunchID, alice, nil); len(list(got, "tags")) != 0 {
		t.Fatalf("deleted tag still attached: %v", got)
	}
	if tags := list(e.must(200, "GET", "/api/v1/tags", alice, nil), "data"); len(tags) != 1 {
		t.Fatalf("tags: %v", tags)
	}

	// Other users' transactions are invisible.
	e.status(404, "GET", "/api/v1/transactions/"+lunchID, mallory, nil)
	e.status(404, "PATCH", "/api/v1/transactions/"+lunchID, mallory, map[string]any{"amount": 1})
	e.status(404, "DELETE", "/api/v1/transactions/"+lunchID, mallory, nil)
	e.status(404, "POST", "/api/v1/transactions/"+lunchID+"/duplicate", mallory, nil)
	if got := list(e.must(200, "GET", "/api/v1/transactions", mallory, nil), "data"); len(got) != 0 {
		t.Fatalf("mallory sees alice's transactions: %v", got)
	}

	e.status(200, "DELETE", "/api/v1/transactions/"+lunchID, alice, nil)
	e.status(404, "DELETE", "/api/v1/transactions/"+lunchID, alice, nil)

	// Idempotent create: a retried POST with the same key does not duplicate.
	hdr := map[string]string{"Idempotency-Key": "wallet-tx-retry-1"}
	body := map[string]any{"type": "EXPENSE", "account_id": bank, "amount": 42}
	s1, b1 := e.raw("POST", "/api/v1/transactions", alice.token, body, hdr)
	s2, b2 := e.raw("POST", "/api/v1/transactions", alice.token, body, hdr)
	if s1 != 201 || s2 != 201 || string(b1) != string(b2) {
		t.Fatalf("idempotent replay: %d %s / %d %s", s1, b1, s2, b2)
	}
	if got := q("min=42&max=42"); len(got) != 1 {
		t.Fatalf("idempotent create duplicated: %v", got)
	}
}
