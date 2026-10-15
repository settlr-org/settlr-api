package wallet_test

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/settlr-org/settlr-api/internal/testutil"
)

func TestTmpPerf(t *testing.T) {
	if os.Getenv("REPORT_PERF") == "" {
		t.Skip()
	}
	ctx := context.Background()
	e := newEnv(t, testutil.CleanDB(t))
	alice := e.register("Alice", "alice-perf@test.local")
	e.useBase("USD", alice)
	for i := 0; i < 12; i++ {
		e.createAccount(alice, "Acc"+string(rune('A'+i)), "USD", 0)
	}
	exec := func(q string, args ...any) {
		if _, err := e.pool.Exec(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO tags (user_id, name, color, icon, sort_order) SELECT $1::uuid, 'tag' || i, '#000', '', i FROM generate_series(1,10) i`, alice.id)
	exec(`INSERT INTO transactions (user_id, type, account_id, amount, currency, title, description, occurred_at, source)
	SELECT $1::uuid, 'EXPENSE', a.id, 100 + (i * 37) % 90000, 'USD',
	       'Shop ' || (i % 5000) || ' purchase ' || md5(i::text), 'note ' || md5((i*7)::text),
	       TIMESTAMPTZ '2020-01-01' + (i * interval '8 minutes'), 'MANUAL'
	FROM generate_series(1, 500000) i
	JOIN LATERAL (SELECT id FROM accounts WHERE user_id = $1::uuid ORDER BY id OFFSET (i % 12) LIMIT 1) a ON true`, alice.id)
	exec(`INSERT INTO transaction_tags (transaction_id, tag_id)
	      SELECT t.id, (SELECT id FROM tags WHERE user_id = $1::uuid ORDER BY id OFFSET (abs(hashtext(t.id::text)) % 10) LIMIT 1)
	      FROM transactions t WHERE t.user_id = $1::uuid AND abs(hashtext(t.id::text)) % 10 < 3`, alice.id)
	exec(`ANALYZE`)
	uid := alice.id.String()
	run := func(label, where string) {
		sql := fmt.Sprintf(`EXPLAIN (ANALYZE, COSTS OFF) SELECT le.id FROM ledger_entries le WHERE le.user_id = '%s' AND le.occurred_at IS NOT NULL AND %s ORDER BY le.sort_at DESC, le.id DESC LIMIT 50`, uid, where)
		rows, err := e.pool.Query(ctx, sql)
		if err != nil {
			t.Fatal(label, err)
		}
		var last string
		var used []string
		for rows.Next() {
			var l string
			rows.Scan(&l)
			if strings.Contains(l, "Execution Time") {
				last = l
			}
			if strings.Contains(l, "Bitmap Index Scan on idx") {
				used = append(used, strings.TrimSpace(l))
			}
		}
		rows.Close()
		t.Logf("%-34s %s %v", label, last, used)
	}
	oldQ := func(terms string) string {
		basic := "(le.title || ' ' || le.description)"
		tags := "COALESCE((SELECT string_agg(g.name, ' ') FROM transaction_tags tt JOIN tags g ON g.id = tt.tag_id WHERE le.kind = 'TRANSACTION' AND tt.transaction_id = le.id), '')"
		return fmt.Sprintf("(%s ILIKE ALL(%s) OR (%s || ' ' || %s) ILIKE ALL(%s))", basic, terms, basic, tags, terms)
	}
	newQ := func(terms string, n int) string {
		tag := fmt.Sprintf("EXISTS (SELECT 1 FROM transaction_tags tt JOIN tags g ON g.id = tt.tag_id WHERE le.kind = 'TRANSACTION' AND tt.transaction_id = le.id AND g.name ILIKE ANY(%s))", terms)
		full := fmt.Sprintf("(le.title || ' ' || le.description || ' ' || COALESCE((SELECT string_agg(g.name, ' ') FROM transaction_tags tt JOIN tags g ON g.id = tt.tag_id WHERE le.kind = 'TRANSACTION' AND tt.transaction_id = le.id), '')) ILIKE ALL(%s)", terms)
		if n == 1 {
			return fmt.Sprintf("(le.title ILIKE ALL(%s) OR le.description ILIKE ALL(%s) OR %s)", terms, terms, tag)
		}
		return fmt.Sprintf("(le.title ILIKE ALL(%s) OR le.description ILIKE ALL(%s) OR %s)", terms, terms, full)
	}
	cases := []struct{ name, terms string; n int }{
		{"rare single 'purchase 4999'", "ARRAY['%4999 purchase%']", 1},
		{"single 'tag3' (tag only)", "ARRAY['%tag3%']", 1},
		{"single common 'shop'", "ARRAY['%shop%']", 1},
		{"two terms 'shop 17' 'purchase'", "ARRAY['%shop 17%','%purchase%']", 2},
		{"no match 'zzzzqq'", "ARRAY['%zzzzqq%']", 1},
	}
	for _, c := range cases {
		run("OLD "+c.name, oldQ(c.terms))
		run("NEW "+c.name, newQ(c.terms, c.n))
	}
}
