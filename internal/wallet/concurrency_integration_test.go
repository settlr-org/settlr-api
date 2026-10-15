package wallet_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"github.com/settlr-org/settlr-api/internal/testutil"
)

type asyncResult struct {
	status int
	body   map[string]any
	err    error
}

// async sends a request from a goroutine (no t.Fatal off the test goroutine).
func (e *env) async(method, path, token string, body any) <-chan asyncResult {
	ch := make(chan asyncResult, 1)
	go func() {
		var rd io.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			rd = bytes.NewReader(b)
		}
		req, _ := http.NewRequest(method, e.srv.URL+path, rd)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			ch <- asyncResult{err: err}
			return
		}
		defer resp.Body.Close()
		out := map[string]any{}
		raw, _ := io.ReadAll(resp.Body)
		_ = json.Unmarshal(raw, &out)
		ch <- asyncResult{status: resp.StatusCode, body: out}
	}()
	return ch
}

func (e *env) await(ch <-chan asyncResult) asyncResult {
	e.t.Helper()
	select {
	case r := <-ch:
		if r.err != nil {
			e.t.Fatalf("async request: %v", r.err)
		}
		return r
	case <-time.After(10 * time.Second):
		e.t.Fatal("async request did not finish")
	}
	return asyncResult{}
}

// lockTx opens a transaction holding a row lock (sql must lock one row).
func (e *env) lockTx(sql string, args ...any) pgx.Tx {
	e.t.Helper()
	ctx := context.Background()
	tx, err := e.pool.Begin(ctx)
	if err != nil {
		e.t.Fatal(err)
	}
	e.t.Cleanup(func() { _ = tx.Rollback(ctx) })
	if _, err := tx.Exec(ctx, sql, args...); err != nil {
		e.t.Fatalf("%s: %v", sql, err)
	}
	return tx
}

func execTx(t *testing.T, tx pgx.Tx, sql string, args ...any) {
	t.Helper()
	if _, err := tx.Exec(context.Background(), sql, args...); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
}

// waitForLockWaiter waits until some backend of this database is blocked on a lock.
func (e *env) waitForLockWaiter() {
	e.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var n int
		if err := e.pool.QueryRow(context.Background(),
			`SELECT count(*) FROM pg_stat_activity WHERE datname = current_database() AND wait_event_type = 'Lock'`).Scan(&n); err != nil {
			e.t.Fatal(err)
		}
		if n > 0 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	e.t.Fatal("request never waited on the held lock")
}

func (e *env) deletedAt(table, id string) *time.Time {
	e.t.Helper()
	var d *time.Time
	if err := e.pool.QueryRow(context.Background(), `SELECT deleted_at FROM `+table+` WHERE id = $1`, id).Scan(&d); err != nil {
		e.t.Fatal(err)
	}
	return d
}

func TestIntegration_SkipOverdueNeverDeletesConcurrentlyPaid(t *testing.T) {
	e := newEnv(t, testutil.CleanDB(t))
	alice := e.register("Alice", "alice-wallet-race1@test.local")
	bank := str(e.createAccount(alice, "Bank", "NPR", 0)["id"])
	past := time.Now().UTC().Add(-48 * time.Hour).Format(time.RFC3339)

	// Plan occurrence: skip-overdue blocks on the plan lock while it gets paid.
	plan := e.must(201, "POST", "/api/v1/planned", alice, map[string]any{
		"type": "EXPENSE", "account_id": bank, "amount": 100, "start_at": past, "interval_unit": "MONTH",
	})
	pending := str(asMap(plan["next"])["id"])
	tx := e.lockTx(`SELECT 1 FROM planned_payments WHERE id = $1 FOR UPDATE`, str(plan["id"]))
	ch := e.async("POST", "/api/v1/transactions/skip-overdue", alice.token, nil)
	e.waitForLockWaiter()
	execTx(t, tx, `UPDATE transactions SET occurred_at = now() WHERE id = $1`, pending)
	execTx(t, tx, `UPDATE planned_payments SET resolved_until = start_at WHERE id = $1`, str(plan["id"]))
	if err := tx.Commit(context.Background()); err != nil {
		t.Fatal(err)
	}
	if res := e.await(ch); res.status != 200 || num(res.body["skipped"]) != 0 {
		t.Fatalf("skip-overdue after concurrent pay: %v", res)
	}
	if d := e.deletedAt("transactions", pending); d != nil {
		t.Fatal("skip-overdue deleted an occurrence paid concurrently")
	}

	// Standalone planned item: blocked on the transaction lock.
	free := e.must(201, "POST", "/api/v1/transactions", alice, map[string]any{"type": "EXPENSE", "account_id": bank, "amount": 5, "due_at": past})
	tx = e.lockTx(`SELECT 1 FROM transactions WHERE id = $1 FOR UPDATE`, str(free["id"]))
	ch = e.async("POST", "/api/v1/transactions/skip-overdue", alice.token, nil)
	e.waitForLockWaiter()
	execTx(t, tx, `UPDATE transactions SET occurred_at = now() WHERE id = $1`, str(free["id"]))
	if err := tx.Commit(context.Background()); err != nil {
		t.Fatal(err)
	}
	if res := e.await(ch); res.status != 200 || num(res.body["skipped"]) != 0 {
		t.Fatalf("skip-overdue after concurrent pay (standalone): %v", res)
	}
	if d := e.deletedAt("transactions", str(free["id"])); d != nil {
		t.Fatal("skip-overdue deleted a standalone item paid concurrently")
	}

	// Single skip of an item paid while it waited: 409, nothing deleted.
	free2 := e.must(201, "POST", "/api/v1/transactions", alice, map[string]any{"type": "EXPENSE", "account_id": bank, "amount": 6, "due_at": past})
	tx = e.lockTx(`SELECT 1 FROM transactions WHERE id = $1 FOR UPDATE`, str(free2["id"]))
	ch = e.async("POST", "/api/v1/transactions/"+str(free2["id"])+"/skip", alice.token, nil)
	e.waitForLockWaiter()
	execTx(t, tx, `UPDATE transactions SET occurred_at = now() WHERE id = $1`, str(free2["id"]))
	if err := tx.Commit(context.Background()); err != nil {
		t.Fatal(err)
	}
	if res := e.await(ch); res.status != 409 {
		t.Fatalf("skip after concurrent pay: %v", res)
	}
	if d := e.deletedAt("transactions", str(free2["id"])); d != nil {
		t.Fatal("skip deleted an item paid concurrently")
	}
}

func TestIntegration_PlanPatchDoesNotResurrectPaid(t *testing.T) {
	e := newEnv(t, testutil.CleanDB(t))
	alice := e.register("Alice", "alice-wallet-race2@test.local")
	bank := str(e.createAccount(alice, "Bank", "NPR", 0)["id"])
	plan := e.must(201, "POST", "/api/v1/planned", alice, map[string]any{
		"type": "EXPENSE", "account_id": bank, "amount": 100, "start_at": "2030-01-01", "one_time": true,
	})
	pid, pending := str(plan["id"]), str(asMap(plan["next"])["id"])

	// The one-time plan gets paid while the PATCH waits for the plan lock.
	tx := e.lockTx(`SELECT 1 FROM planned_payments WHERE id = $1 FOR UPDATE`, pid)
	ch := e.async("PATCH", "/api/v1/planned/"+pid, alice.token, map[string]any{"title": "Renamed"})
	e.waitForLockWaiter()
	execTx(t, tx, `UPDATE transactions SET occurred_at = now() WHERE id = $1`, pending)
	execTx(t, tx, `UPDATE planned_payments SET active = false, resolved_until = start_at WHERE id = $1`, pid)
	if err := tx.Commit(context.Background()); err != nil {
		t.Fatal(err)
	}
	res := e.await(ch)
	if res.status != 200 || res.body["active"] != false || res.body["next"] != nil || str(res.body["title"]) != "Renamed" {
		t.Fatalf("patch merged onto the locked row: %v", res)
	}
	var n int
	if err := e.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM transactions WHERE planned_payment_id = $1 AND occurred_at IS NULL AND deleted_at IS NULL`, pid).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("a paid one-time plan got a new pending occurrence: %d", n)
	}
}

// Pay and PATCH-occurred_at take the plan lock before the transaction lock (the
// order plan edits use), so they cannot deadlock with them.
func TestIntegration_PlanLockOrder(t *testing.T) {
	e := newEnv(t, testutil.CleanDB(t))
	alice := e.register("Alice", "alice-wallet-race3@test.local")
	bank := str(e.createAccount(alice, "Bank", "NPR", 0)["id"])
	for _, c := range []struct {
		method, suffix string
		body           any
	}{
		{"POST", "/pay", nil},
		{"PATCH", "", map[string]any{"occurred_at": "2030-01-02"}},
		{"POST", "/skip", nil},
		{"DELETE", "", nil},
	} {
		plan := e.must(201, "POST", "/api/v1/planned", alice, map[string]any{
			"type": "EXPENSE", "account_id": bank, "amount": 100, "start_at": "2030-01-01", "interval_unit": "WEEK",
		})
		pending := str(asMap(plan["next"])["id"])
		tx := e.lockTx(`SELECT 1 FROM planned_payments WHERE id = $1 FOR UPDATE`, str(plan["id"]))
		ch := e.async(c.method, "/api/v1/transactions/"+pending+c.suffix, alice.token, c.body)
		e.waitForLockWaiter()
		// While the request waits for the plan, it must not hold the transaction.
		probe, err := e.pool.Begin(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := probe.Exec(context.Background(), `SELECT 1 FROM transactions WHERE id = $1 FOR UPDATE NOWAIT`, pending); err != nil {
			t.Fatalf("%s %s locked the transaction before the plan: %v", c.method, c.suffix, err)
		}
		_ = probe.Rollback(context.Background())
		if err := tx.Commit(context.Background()); err != nil {
			t.Fatal(err)
		}
		if res := e.await(ch); res.status != 200 {
			t.Fatalf("%s %s: %v", c.method, c.suffix, res)
		}
	}
}

func TestIntegration_LoanPatchMergesOntoLockedRow(t *testing.T) {
	e := newEnv(t, testutil.CleanDB(t))
	alice := e.register("Alice", "alice-wallet-race4@test.local")
	loan := e.must(201, "POST", "/api/v1/loans", alice, map[string]any{"name": "Bob", "type": "LEND", "amount": 1000})
	lid := str(loan["id"])
	rec := e.must(201, "POST", "/api/v1/loans/"+lid+"/records", alice, map[string]any{"type": "DECREASE", "amount": 100})

	tx := e.lockTx(`SELECT 1 FROM loans WHERE id = $1 FOR UPDATE`, lid)
	ch := e.async("PATCH", "/api/v1/loans/"+lid, alice.token, map[string]any{"note": "n"})
	e.waitForLockWaiter()
	execTx(t, tx, `UPDATE loans SET amount = 999 WHERE id = $1`, lid)
	if err := tx.Commit(context.Background()); err != nil {
		t.Fatal(err)
	}
	if res := e.await(ch); res.status != 200 || num(res.body["amount"]) != 999 || str(res.body["note"]) != "n" {
		t.Fatalf("loan patch overwrote a concurrent change: %v", res)
	}

	tx = e.lockTx(`SELECT 1 FROM loans WHERE id = $1 FOR UPDATE`, lid)
	ch = e.async("PATCH", "/api/v1/loan-records/"+str(rec["id"]), alice.token, map[string]any{"note": "x"})
	e.waitForLockWaiter()
	execTx(t, tx, `UPDATE loan_records SET amount = 777 WHERE id = $1`, str(rec["id"]))
	if err := tx.Commit(context.Background()); err != nil {
		t.Fatal(err)
	}
	if res := e.await(ch); res.status != 200 || num(res.body["amount"]) != 777 || str(res.body["note"]) != "x" {
		t.Fatalf("record patch overwrote a concurrent change: %v", res)
	}
}

func TestIntegration_BudgetEditableAfterReferencesDeleted(t *testing.T) {
	e := newEnv(t, testutil.CleanDB(t))
	alice := e.register("Alice", "alice-wallet-budget2@test.local")
	bob := e.register("Bob", "bob-wallet-budget2@test.local")
	acct := str(e.createAccount(alice, "Old", "NPR", 0)["id"])
	bobAcct := str(e.createAccount(bob, "Bank", "NPR", 0)["id"])
	cat := str(e.must(201, "POST", "/api/v1/categories", alice, map[string]any{"name": "Pets"})["id"])
	b := e.must(201, "POST", "/api/v1/budgets", alice, map[string]any{"name": "B", "amount": 100, "category_ids": []string{cat}, "account_ids": []string{acct}})
	e.must(200, "DELETE", "/api/v1/accounts/"+acct, alice, nil)
	e.must(200, "DELETE", "/api/v1/categories/"+cat, alice, nil)
	got := e.must(200, "PATCH", "/api/v1/budgets/"+str(b["id"]), alice, map[string]any{"name": "Renamed"})
	if str(got["name"]) != "Renamed" || len(got["account_ids"].([]any)) != 1 {
		t.Fatalf("budget with stale ids still editable: %v", got)
	}
	// Newly added ids are still validated; stale ones can be pruned.
	e.status(422, "PATCH", "/api/v1/budgets/"+str(b["id"]), alice, map[string]any{"account_ids": []string{acct, bobAcct}})
	got = e.must(200, "PATCH", "/api/v1/budgets/"+str(b["id"]), alice, map[string]any{"account_ids": []string{}, "category_ids": []string{}})
	if len(got["account_ids"].([]any)) != 0 || len(got["category_ids"].([]any)) != 0 {
		t.Fatalf("pruned: %v", got)
	}
}

// Rolling 0015 back drops plan-generated pending occurrences (they would show up
// as standalone due items in the previous release) but keeps paid ones.
func TestIntegration_PlanningDownMigration(t *testing.T) {
	pool := testutil.CleanDB(t)
	e := newEnv(t, pool)
	alice := e.register("Alice", "alice-wallet-down@test.local")
	bank := str(e.createAccount(alice, "Bank", "NPR", 0)["id"])
	plan := e.must(201, "POST", "/api/v1/planned", alice, map[string]any{
		"type": "EXPENSE", "account_id": bank, "amount": 100, "start_at": "2030-01-01", "interval_unit": "MONTH",
	})
	res := e.must(200, "POST", "/api/v1/transactions/"+str(asMap(plan["next"])["id"])+"/pay", alice, nil)
	paid, pending := str(asMap(res["transaction"])["id"]), str(asMap(res["next"])["id"])
	free := str(e.must(201, "POST", "/api/v1/transactions", alice, map[string]any{"type": "EXPENSE", "account_id": bank, "amount": 5, "due_at": "2030-01-01"})["id"])

	ctx := context.Background()
	sqlDB := stdlib.OpenDBFromPool(pool)
	provider, err := goose.NewProvider(goose.DialectPostgres, sqlDB, os.DirFS("../../migrations"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		defer sqlDB.Close()
		if _, err := provider.Up(ctx); err != nil {
			t.Errorf("re-migrate: %v", err)
		}
	})
	if _, err := provider.DownTo(ctx, 14); err != nil {
		t.Fatalf("down to 14: %v", err)
	}
	if e.deletedAt("transactions", pending) == nil {
		t.Fatal("plan pending occurrence survived the rollback")
	}
	if e.deletedAt("transactions", paid) != nil || e.deletedAt("transactions", free) != nil {
		t.Fatal("rollback deleted a paid occurrence or a standalone planned item")
	}
}
