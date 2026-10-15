package wallet_test

import (
	"context"
	"fmt"
	"math"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/settlr-org/settlr-api/internal/testutil"
)

// A removed member keeps their group entries (amounts, titles, balances) but other people's
// free text (expense notes, settlement notes) comes back empty everywhere the ledger is read.
func TestIntegration_FormerGroupMemberNotesAreRedacted(t *testing.T) {
	e := newEnv(t, testutil.CleanDB(t))
	alice := e.register("Alice", "alice-redact@test.local")
	bob := e.register("Bob", "bob-redact@test.local")
	e.makeFriends(alice.id, bob.id)
	e.useBase("USD", alice, bob)
	g := e.must(201, "POST", "/api/v1/groups", alice, map[string]any{"name": "Trip", "currency": "USD"})
	gid := str(g["id"])
	e.must(201, "POST", "/api/v1/groups/"+gid+"/members", alice, map[string]any{"user_id": bob.id.String()})
	wallet := str(e.createAccount(bob, "Wallet", "USD", 0)["id"])
	splits := []map[string]string{{"user_id": alice.id.String()}, {"user_id": bob.id.String()}}
	e.must(201, "POST", "/api/v1/groups/"+gid+"/expenses", bob, map[string]any{
		"description": "Dinner", "notes": "ALICE-HOME-ADDRESS-SECRET", "amount": 10000, "paid_by": bob.id.String(),
		"split_mode": "EQUAL", "splits": splits, "expense_date": "2026-05-10", "payer_account_id": wallet})
	e.must(201, "POST", "/api/v1/groups/"+gid+"/settlements", alice, map[string]any{
		"from_user": alice.id.String(), "to_user": bob.id.String(), "amount": 500, "note": "SETTLE-NOTE-SECRET"})
	bobAcc := func() string {
		return fmt.Sprint(e.accountsByName(bob)["Wallet"]["balance"])
	}
	before := bobAcc()
	csvOf := func(u user) string {
		s, raw := e.raw("GET", "/api/v1/reports/export.csv", u.token, nil, nil)
		if s != 200 {
			t.Fatalf("csv %d", s)
		}
		return string(raw)
	}
	descs := func(u user) map[string]string {
		out := map[string]string{}
		for _, le := range list(e.must(200, "GET", "/api/v1/ledger?kind=GROUP_EXPENSE,SETTLEMENT&exclude_source=ADJUSTMENT", u, nil), "data") {
			out[str(le["title"])] = str(le["description"])
		}
		return out
	}
	// Member: everything visible.
	if d := descs(bob); d["Dinner"] != "ALICE-HOME-ADDRESS-SECRET" {
		t.Fatalf("member sees notes: %v", d)
	}
	if !strings.Contains(csvOf(bob), "ALICE-HOME-ADDRESS-SECRET") {
		t.Fatal("member csv should have notes")
	}
	e.status(200, "DELETE", "/api/v1/groups/"+gid+"/members/"+bob.id.String(), alice, nil)
	// Former member: amounts, titles and balances stay, notes are gone.
	d := descs(bob)
	if _, ok := d["Dinner"]; !ok || d["Dinner"] != "" {
		t.Fatalf("former member must keep the entry with empty notes: %v", d)
	}
	for _, le := range list(e.must(200, "GET", "/api/v1/ledger?kind=GROUP_EXPENSE", bob, nil), "data") {
		if num(le["amount"]) != 5000 || str(le["title"]) != "Dinner" || str(le["group_name"]) != "Trip" {
			t.Fatalf("entry changed: %v", le)
		}
	}
	for _, body := range []string{csvOf(bob), fmt.Sprint(e.must(200, "GET", "/api/v1/ledger", bob, nil))} {
		if strings.Contains(body, "SECRET") {
			t.Fatalf("former member still reads other people's text: %s", body)
		}
		if !strings.Contains(body, "Dinner") {
			t.Fatalf("title must stay: %s", body)
		}
	}
	if after := bobAcc(); after != before {
		t.Fatalf("balance changed by removal: %s -> %s", before, after)
	}
	if r := e.must(200, "GET", "/api/v1/reports", bob, nil); num(r["expense"]) != 5000 {
		t.Fatalf("former member keeps their expense total: %v", r)
	}
	// Search and the group export need current membership.
	if s := fmt.Sprint(e.must(200, "GET", "/api/v1/search?q=Dinner", bob, nil)["expenses"]); strings.Contains(s, "Dinner") {
		t.Fatalf("search still finds group expense: %s", s)
	}
	// (The group export and the v2 backup join group_members for the caller, see export.sql.)
	// Current member (the payer's counterpart) still sees her notes.
	if d := descs(alice); d["Dinner"] != "ALICE-HOME-ADDRESS-SECRET" {
		t.Fatalf("current member: %v", d)
	}
}

// Every range filter uses COALESCE(occurred_at, due_at): a group share dated May 10 sits at
// 2026-05-10T00:00:00Z whatever time of day it was created, so overview, report, ledger paging
// and the CSV agree for windows that do not start at UTC midnight.
func TestIntegration_NonUTCWindowsAgree(t *testing.T) {
	f := newReportFixture(t)
	e := f.e
	windows := []struct {
		from, to string
		expense  int64
		entries  int64
	}{
		{"2026-05-10T00:00:00+05:45", "2026-05-11T00:00:00+05:45", 150, 2},  // 05-09T18:15Z .. 05-10T18:15Z holds both shares
		{"2026-05-10T00:00:30Z", "2026-05-11T00:00:30Z", 0, 0},              // starts 30 s after the shares
		{"2026-05-09T23:59:30Z", "2026-05-10T00:00:30Z", 150, 2},            // straddles midnight UTC
		{"2026-05-10T00:00:00Z", "2026-05-10T00:00:00Z", 0, 0},              // empty range is invalid, see below
		{"2026-05-06T12:00:00Z", "2026-05-06T12:00:01Z", 1000, 1},           // the Momo transaction at exactly 12:00:00Z
		{"2026-05-06T06:00:00-06:00", "2026-05-06T06:00:01-06:00", 1000, 1}, // same instant, other offset
	}
	for _, w := range windows {
		q := "from=" + url.QueryEscape(w.from) + "&to=" + url.QueryEscape(w.to)
		if w.from == w.to {
			e.status(422, "GET", "/api/v1/reports?"+q, f.alice, nil)
			continue
		}
		rep := f.report(f.alice, q)
		ov := e.must(200, "GET", "/api/v1/overview?"+q, f.alice, nil)
		inc, exp, cnt := ledgerSum(t, e, f.alice, q)
		_, raw := e.raw("GET", "/api/v1/reports/export.csv?"+q+"&exclude_source=ADJUSTMENT,LOAN&type=INCOME,EXPENSE", f.alice.token, nil, nil)
		csvRows := 0
		for _, r := range readCSV(t, raw)[1:] {
			if r[10] != "0.00" {
				csvRows++
			}
		}
		if num(rep["expense"]) != w.expense || num(ov["expense"]) != w.expense || exp != w.expense || inc != 0 ||
			num(rep["count"]) != w.entries || cnt != w.entries || int64(csvRows) != w.entries {
			t.Errorf("%s..%s: report=%v overview=%v ledger=%d/%d csv=%d want %d in %d entries",
				w.from, w.to, rep["expense"], ov["expense"], exp, cnt, csvRows, w.expense, w.entries)
		}
	}
}

// A share dated today is created today (created_at inside the day), which used to make /ledger sort
// and filter it at creation time while the overview used midnight UTC; all views now use midnight.
func TestIntegration_ShareDatedTodayUsesDayStart(t *testing.T) {
	f := newReportFixture(t)
	e := f.e
	g := e.must(201, "POST", "/api/v1/groups", f.alice, map[string]any{"name": "Today", "currency": "NPR"})
	gid := str(g["id"])
	e.must(201, "POST", "/api/v1/groups/"+gid+"/members", f.alice, map[string]any{"user_id": f.bob.id.String()})
	now := time.Now().UTC()
	today, tomorrow := now.Format("2006-01-02"), now.AddDate(0, 0, 1).Format("2006-01-02")
	e.must(201, "POST", "/api/v1/groups/"+gid+"/expenses", f.alice, map[string]any{
		"description": "Today", "amount": 13300, "paid_by": f.alice.id.String(), "split_mode": "EQUAL", "expense_date": today,
		"splits": []map[string]string{{"user_id": f.alice.id.String()}, {"user_id": f.bob.id.String()}}})
	check := func(q string, want int64) {
		t.Helper()
		rep := f.report(f.alice, q)
		ov := e.must(200, "GET", "/api/v1/overview?"+q, f.alice, nil)
		_, exp, _ := ledgerSum(t, e, f.alice, q)
		if num(rep["expense"]) != want || num(ov["expense"]) != want || exp != want {
			t.Fatalf("%s: report=%v overview=%v ledger=%d want %d", q, rep["expense"], ov["expense"], exp, want)
		}
	}
	check("from="+today+"T00:00:00Z&to="+tomorrow+"T00:00:00Z", 50)
	check("from="+today+"T00:00:30Z&to="+tomorrow+"T00:00:30Z", 0)
	check("from="+today+"&to="+today, 50)
}

// Shared filter parser: one raw search-box value works everywhere (truncated, never rejected).
func TestIntegration_LongKeywordsAreTruncatedEverywhere(t *testing.T) {
	f := newReportFixture(t)
	e := f.e
	q := url.QueryEscape("lunch pizza a b c d e f g h i j k l m " + strings.Repeat("x", 150))
	for _, p := range []string{"/api/v1/ledger?q=", "/api/v1/transactions?q=", "/api/v1/reports?q=", "/api/v1/reports/trend?from=2026-05-01&to=2026-05-31&q=", "/api/v1/reports/export.csv?q="} {
		if s, raw := e.raw("GET", p+q, f.alice.token, nil, nil); s != 200 {
			t.Fatalf("%s: %d %s", p, s, raw)
		}
		if s, _ := e.raw("GET", p+"x&excl_q="+q, f.alice.token, nil, nil); s != 200 {
			t.Fatalf("%s excl_q: %d", p, s)
		}
	}
	// Only the first 10 terms count: "lunch pizza a b c d e f g h" must match "Lunch pizza"
	// only if every one of them matches, which they do not ("a".."h" appear nowhere together) -> 0;
	// but lunch + pizza + 8 letters that all occur in the title/description of the pizza row do match.
	e.must(201, "POST", "/api/v1/transactions", f.alice, map[string]any{"type": "EXPENSE", "account_id": f.bank, "amount": 3, "title": "abcdefgh ij", "description": "k", "occurred_at": "2026-05-12"})
	got := num(f.report(f.alice, may+"&q="+url.QueryEscape("abcdefgh ij k a b c d e f g h i j NOPE-ELEVENTH"))["count"])
	if got != 1 {
		t.Fatalf("11th term must be ignored: count %d", got)
	}
}

// /transactions and /ledger(kind=TRANSACTION) answer the same filters with the same rows in the same order.
func TestIntegration_TransactionsMatchLedger(t *testing.T) {
	f := newReportFixture(t)
	e := f.e
	e.must(201, "POST", "/api/v1/transactions", f.alice, map[string]any{"type": "EXPENSE", "account_id": f.bank, "amount": 10000, "title": "Pizza again 100%", "occurred_at": "2026-05-03"})
	ids := func(base, q string) []string {
		var out []string
		cursor := ""
		for i := 0; i < 50; i++ {
			path := base + "?limit=3&" + q
			if cursor != "" {
				path += "&cursor=" + url.QueryEscape(cursor)
			}
			res := e.must(200, "GET", path, f.alice, nil)
			for _, r := range list(res, "data") {
				out = append(out, str(r["id"]))
			}
			cursor, _ = res["next_cursor"].(string)
			if cursor == "" {
				return out
			}
		}
		t.Fatal("paging")
		return nil
	}
	for _, q := range []string{
		"", may, "type=EXPENSE", "type=TRANSFER", "category=" + f.food, "category=none", "category=none," + f.rent,
		"account=" + f.bank, "account=" + f.euro, "tag=" + f.t1, "excl_tag=" + f.t1, "q=pizza", "q=pizza+10000", "q=lunch+pizza", "q=holiday", "q=%25", "excl_q=pizza", "excl_q=holiday+rent",
		"q=pizza&excl_q=again", "min=5000", "max=9000", "min=5000&max=10000", "source=MANUAL", "exclude_source=ADJUSTMENT", "hide_adjustments=true", "planned=true", "planned=all",
		"from=2026-05-03T00:00:00%2B05:45&to=2026-05-07",
	} {
		a, b := ids("/api/v1/transactions", q), ids("/api/v1/ledger", "kind=TRANSACTION&"+q)
		if strings.Join(a, ",") != strings.Join(b, ",") {
			t.Errorf("%q: transactions %d rows vs ledger %d rows differ\n%v\n%v", q, len(a), len(b), a, b)
		}
	}
}

// Currency conversion happens once per currency pair on the SUM, not per entry: the report can
// differ from a per-entry sum by at most one minor unit per entry.
func TestIntegration_ReportRoundsPerCurrencyPair(t *testing.T) {
	e := newEnv(t, testutil.CleanDB(t))
	alice := e.register("Alice", "alice-rep-round@test.local")
	e.useBase("USD", alice)
	npr := str(e.createAccount(alice, "NPR", "NPR", 0)["id"])
	for i := 0; i < 3; i++ { // each 100 NPR-minor = 0.75 USD-minor
		e.must(201, "POST", "/api/v1/transactions", alice, map[string]any{"type": "EXPENSE", "account_id": npr, "amount": 100, "occurred_at": "2026-05-02"})
	}
	r := e.must(200, "GET", "/api/v1/reports?"+may, alice, nil)
	perPair := int64(math.Round(300.0 / 133))
	perEntry := 3 * int64(math.Round(100.0/133))
	if num(r["expense"]) != perPair || perPair == perEntry {
		t.Fatalf("expense %v, per-pair %d, per-entry %d", r["expense"], perPair, perEntry)
	}
	if d := perEntry - num(r["expense"]); d > 3 || d < -3 {
		t.Fatalf("difference beyond one minor unit per entry: %d", d)
	}
}

// useBase sets the users' base (default) currency; new users start in NPR.
func (e *env) useBase(currency string, users ...user) {
	e.t.Helper()
	for _, u := range users {
		if _, err := e.pool.Exec(context.Background(), `UPDATE users SET default_currency = $1 WHERE id = $2`, currency, u.id); err != nil {
			e.t.Fatal(err)
		}
	}
}
