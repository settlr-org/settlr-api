package expenses

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/settlr-org/settlr-api/internal/activity"
	"github.com/settlr-org/settlr-api/internal/auth"
	"github.com/settlr-org/settlr-api/internal/config"
	db "github.com/settlr-org/settlr-api/internal/db"
	"github.com/settlr-org/settlr-api/internal/recurring"
	"github.com/settlr-org/settlr-api/internal/testutil"
)

func doJSON(t *testing.T, method, url, token string, body any, extra map[string]string) (int, map[string]any) {
	t.Helper()
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest(method, url, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	for k, v := range extra {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

// User text containing quotes used to be spliced into activity JSON by string concatenation,
// producing invalid JSONB and a 500; payloads were also returned base64-encoded. Recurring
// expenses without a category wrote a zero UUID into the categories FK and failed.
func TestIntegration_Regression_QuotedTextActivityAndRecurring(t *testing.T) {
	pool := testutil.CleanDB(t)
	srv, tokenFor := newTestServer(t, pool)
	defer srv.Close()

	// Mount the extra handlers this regression covers on a second server sharing the pool.
	cfg := config.Config{JWTSecret: "test-jwt-secret-32-chars-min!!!!", JWTRefreshSecret: "test-refresh-secret-32-chars!!", JWTExpiryMinutes: 15, RefreshExpiryDays: 30, Env: "test"}
	authMw := auth.Middleware(&auth.Service{Pool: pool, Cfg: cfg})
	mux := http.NewServeMux()
	rec := &recurring.Handler{Pool: pool, Queries: db.New(pool)}
	rec.RegisterRoutes(mux, authMw)
	(&activity.Handler{Pool: pool, Queries: db.New(pool)}).RegisterRoutes(mux, authMw)
	extra := httptest.NewServer(mux)
	defer extra.Close()

	alice := registerUserViaAPI(t, srv, "Alice", "alice-reg@test.local", "password123")
	tok := tokenFor(alice)

	code, group := doJSON(t, "POST", srv.URL+"/api/v1/groups", tok, map[string]string{"name": `The "Flat"`, "currency": "NPR"}, nil)
	if code != 201 {
		t.Fatalf("create group with quotes: %d %v", code, group)
	}
	gid := group["id"].(string)

	code, exp := doJSON(t, "POST", fmt.Sprintf("%s/api/v1/groups/%s/expenses", srv.URL, gid), tok, map[string]any{
		"description": `6" pizza \ "large"`, "amount": 1200, "split_mode": "EQUAL", "paid_by": alice.String(),
		"splits": []map[string]string{{"user_id": alice.String()}},
	}, map[string]string{"Idempotency-Key": uuid.NewString()})
	if code != 201 {
		t.Fatalf("create expense with quotes: %d %v", code, exp)
	}

	code, feed := doJSON(t, "GET", extra.URL+"/api/v1/activity?limit=10", tok, nil, nil)
	if code != 200 {
		t.Fatalf("activity: %d %v", code, feed)
	}
	var sawExpense bool
	for _, raw := range feed["data"].([]any) {
		ev := raw.(map[string]any)
		payload, ok := ev["payload"].(map[string]any)
		if !ok {
			t.Fatalf("payload should be a JSON object, got %T (%v)", ev["payload"], ev["payload"])
		}
		if ev["type"] == "EXPENSE_ADDED" {
			sawExpense = true
			if payload["description"] != `6" pizza \ "large"` || payload["amount"].(float64) != 1200 || payload["currency"] != "NPR" {
				t.Fatalf("unexpected expense payload %v", payload)
			}
		}
	}
	if !sawExpense {
		t.Fatal("EXPENSE_ADDED event missing")
	}

	code, created := doJSON(t, "POST", fmt.Sprintf("%s/api/v1/groups/%s/recurring", extra.URL, gid), tok, map[string]any{
		"description": "Wifi", "amount": 2000, "paid_by": alice.String(), "frequency": "MONTHLY",
		"start_date": time.Now().UTC().Format("2006-01-02"), "splits": []map[string]string{{"user_id": alice.String()}},
	}, nil)
	if code != 201 {
		t.Fatalf("recurring without category: %d %v", code, created)
	}

	// The due schedule must materialise into an expense with a NULL category.
	rec.RunDue(context.Background())
	var n int
	if err := pool.QueryRow(context.Background(),
		`SELECT count(*) FROM expenses WHERE group_id=$1 AND description='Wifi (recurring)' AND category_id IS NULL`, gid).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("recurring instance expenses = %d, want 1", n)
	}
}

// Percentages were converted with int64(p*100), truncating 29.29 → 2928 bps, so a split that
// sums to exactly 100% was rejected ("percentages must sum to 10000").
func TestIntegration_Regression_PercentageRounding(t *testing.T) {
	pool := testutil.CleanDB(t)
	srv, tokenFor := newTestServer(t, pool)
	defer srv.Close()
	alice := registerUserViaAPI(t, srv, "Alice", "alice-pct@test.local", "password123")
	bob := registerUserViaAPI(t, srv, "Bob", "bob-pct@test.local", "password123")
	ensureFriends(t, pool, alice, bob)
	tok := tokenFor(alice)

	code, group := doJSON(t, "POST", srv.URL+"/api/v1/groups", tok, map[string]string{"name": "Pct", "currency": "NPR"}, nil)
	if code != 201 {
		t.Fatalf("create group: %d %v", code, group)
	}
	gid := group["id"].(string)
	if code, out := doJSON(t, "POST", fmt.Sprintf("%s/api/v1/groups/%s/members", srv.URL, gid), tok, map[string]string{"user_id": bob.String()}, nil); code >= 300 {
		t.Fatalf("add member: %d %v", code, out)
	}
	code, exp := doJSON(t, "POST", fmt.Sprintf("%s/api/v1/groups/%s/expenses", srv.URL, gid), tok, map[string]any{
		"description": "Rent", "amount": 10000, "split_mode": "PERCENTAGE", "paid_by": alice.String(),
		"splits": []map[string]any{{"user_id": alice.String(), "percentage": 70.71}, {"user_id": bob.String(), "percentage": 29.29}},
	}, map[string]string{"Idempotency-Key": uuid.NewString()})
	if code != 201 {
		t.Fatalf("percentage split summing to 100 rejected: %d %v", code, exp)
	}
}

// A schedule that started long ago must produce one instance (its latest occurrence) and move
// next_run_at into the future — not back-fill every missed period. A broken schedule must not
// make RunDue spin or block healthy schedules.
func TestIntegration_Regression_RecurringCatchUpAndFailures(t *testing.T) {
	pool := testutil.CleanDB(t)
	srv, tokenFor := newTestServer(t, pool)
	defer srv.Close()
	alice := registerUserViaAPI(t, srv, "Alice", "alice-rec@test.local", "password123")
	tok := tokenFor(alice)
	code, group := doJSON(t, "POST", srv.URL+"/api/v1/groups", tok, map[string]string{"name": "Rec", "currency": "NPR"}, nil)
	if code != 201 {
		t.Fatalf("create group: %d", code)
	}
	gid := group["id"].(string)
	ctx := context.Background()

	if _, err := pool.Exec(ctx, `INSERT INTO recurring_expenses (id, group_id, created_by, description, amount, currency, split_mode, splits, paid_by, frequency, next_run_at)
		VALUES (gen_random_uuid(), $1, $2, 'Rent', 1000, 'NPR', 'EQUAL', '[]', $2, 'MONTHLY', '2020-01-15T00:00:00Z')`, gid, alice); err != nil {
		t.Fatal(err)
	}
	// Broken: its stored split names a user that does not exist, so inserting the split
	// violates a FK on every attempt.
	if _, err := pool.Exec(ctx, `INSERT INTO recurring_expenses (id, group_id, created_by, description, amount, currency, split_mode, splits, paid_by, frequency, next_run_at)
		VALUES (gen_random_uuid(), $1, $2, 'Broken', 1000, 'NPR', 'EXACT', jsonb_build_array(jsonb_build_object('user_id', gen_random_uuid()::text, 'amount', 1000)), $2, 'DAILY', now() - interval '1 hour')`, gid, alice); err != nil {
		t.Fatalf("broken fixture: %v", err)
	}

	rec := &recurring.Handler{Pool: pool, Queries: db.New(pool)}
	done := make(chan struct{})
	go func() { rec.RunDue(ctx); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("RunDue did not return: failing schedule caused a hot loop")
	}

	var n int
	var date time.Time
	if err := pool.QueryRow(ctx, `SELECT count(*), coalesce(max(expense_date), '1970-01-01') FROM expenses WHERE group_id=$1 AND description='Rent (recurring)'`, gid).Scan(&n, &date); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("rent instances = %d, want exactly 1 (no back-fill)", n)
	}
	now := time.Now().UTC()
	if date.Day() != 15 || date.After(now) || now.Sub(date) > 32*24*time.Hour {
		t.Fatalf("instance dated %v, want the latest 15th on or before today", date)
	}
	var future bool
	if err := pool.QueryRow(ctx, `SELECT next_run_at > now() FROM recurring_expenses WHERE description='Rent'`).Scan(&future); err != nil || !future {
		t.Fatalf("next_run_at should be in the future: %v %v", future, err)
	}
	// A second run creates nothing new.
	rec.RunDue(ctx)
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM expenses WHERE group_id=$1 AND description='Rent (recurring)'`, gid).Scan(&n)
	if n != 1 {
		t.Fatalf("second run created more instances: %d", n)
	}
}
