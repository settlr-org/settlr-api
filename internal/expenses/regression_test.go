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
