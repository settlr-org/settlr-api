package wallet_test

import (
	"fmt"
	"net/url"
	"testing"

	"github.com/settlr-org/settlr-api/internal/testutil"
)

func TestIntegration_SourceFilters(t *testing.T) {
	e := newEnv(t, testutil.CleanDB(t))
	u := e.register("Sam", "sam-src-filter@test.local")
	acc := str(e.createAccount(u, "Bank", "USD", 50000)["id"]) // creates an ADJUSTMENT (initial balance)
	for i := 1; i <= 5; i++ {
		e.must(201, "POST", "/api/v1/transactions", u, map[string]any{
			"type": "EXPENSE", "account_id": acc, "amount": 100 * i, "title": fmt.Sprintf("Coffee %d", i),
			"occurred_at": fmt.Sprintf("2026-06-%02dT10:00:00Z", i),
		})
	}
	e.must(200, "POST", "/api/v1/accounts/"+acc+"/adjust", u, map[string]any{"balance": 12345})

	sources := func(path string) []string {
		var out []string
		for _, r := range list(e.must(200, "GET", path, u, nil), "data") {
			out = append(out, str(r["source"]))
		}
		return out
	}
	count := func(path, src string) int {
		n := 0
		for _, s := range sources(path) {
			if src == "" || s == src {
				n++
			}
		}
		return n
	}

	for _, base := range []string{"/api/v1/transactions", "/api/v1/ledger"} {
		all := len(sources(base))
		adj := count(base, "ADJUSTMENT")
		if adj < 2 || all != adj+5 {
			t.Fatalf("%s: unfiltered all=%d adjustments=%d", base, all, adj)
		}
		for _, q := range []string{"?hide_adjustments=true", "?exclude_source=ADJUSTMENT", "?exclude_source=adjustment,LOAN"} {
			got := sources(base + q)
			if len(got) != 5 || count(base+q, "ADJUSTMENT") != 0 {
				t.Fatalf("%s%s: %v", base, q, got)
			}
		}
		if got := sources(base + "?source=ADJUSTMENT"); len(got) != adj {
			t.Fatalf("%s source=ADJUSTMENT: %v", base, got)
		}
		if got := sources(base + "?source=MANUAL,ADJUSTMENT&exclude_source=ADJUSTMENT"); len(got) != 5 {
			t.Fatalf("%s source+exclude: %v", base, got)
		}
		if got := sources(base + "?hide_adjustments=false"); len(got) != all {
			t.Fatalf("%s hide_adjustments=false: %v", base, got)
		}
		for _, bad := range []string{"?source=BOGUS", "?exclude_source=MANUAL,NOPE", "?hide_adjustments=maybe"} {
			e.status(422, "GET", base+bad, u, nil)
		}

		// Cursor pagination stays correct with the filter applied.
		var seen []string
		cursor := ""
		for page := 0; page < 10; page++ {
			q := base + "?hide_adjustments=true&limit=2"
			if cursor != "" {
				q += "&cursor=" + url.QueryEscape(cursor)
			}
			out := e.must(200, "GET", q, u, nil)
			for _, r := range list(out, "data") {
				if str(r["source"]) == "ADJUSTMENT" {
					t.Fatalf("%s: adjustment leaked on page %d", base, page)
				}
				seen = append(seen, str(r["id"]))
			}
			c, _ := out["next_cursor"].(string)
			if c == "" {
				break
			}
			cursor = c
		}
		uniq := map[string]bool{}
		for _, id := range seen {
			uniq[id] = true
		}
		if len(seen) != 5 || len(uniq) != 5 {
			t.Fatalf("%s: paged %d rows (%d unique), want 5", base, len(seen), len(uniq))
		}
	}
	// GROUP is a ledger-only source.
	e.status(200, "GET", "/api/v1/ledger?source=GROUP", u, nil)
	e.status(422, "GET", "/api/v1/transactions?source=GROUP", u, nil)
}
