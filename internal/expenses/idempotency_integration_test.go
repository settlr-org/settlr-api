package expenses

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"testing"

	"github.com/google/uuid"

	"github.com/settlr-org/settlr-api/internal/testutil"
)

// The Android offline outbox retries writes with the same Idempotency-Key; a retried
// create must replay the original response instead of inserting a second expense.
func TestIntegration_IdempotentCreateExpense(t *testing.T) {
	pool := testutil.CleanDB(t)
	srv, tokenFor := newTestServer(t, pool)
	defer srv.Close()

	alice := registerUserViaAPI(t, srv, "Alice", "alice-idem@test.local", "password123")
	tok := tokenFor(alice)
	code, group := doJSON(t, "POST", srv.URL+"/api/v1/groups", tok, map[string]string{"name": "Idem", "currency": "NPR"}, nil)
	if code != 201 {
		t.Fatalf("create group: %d %v", code, group)
	}
	gid := group["id"].(string)

	body, _ := json.Marshal(map[string]any{
		"description": "Momo", "amount": 900, "split_mode": "EQUAL", "paid_by": alice.String(),
		"splits": []map[string]string{{"user_id": alice.String()}},
	})
	key := uuid.NewString()
	post := func() (int, string, string) {
		req, _ := http.NewRequest("POST", fmt.Sprintf("%s/api/v1/groups/%s/expenses", srv.URL, gid), bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+tok)
		req.Header.Set("Idempotency-Key", key)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b), resp.Header.Get("Idempotent-Replayed")
	}

	c1, b1, r1 := post()
	c2, b2, r2 := post()
	if c1 != 201 || r1 != "" {
		t.Fatalf("first create: %d %s replayed=%q", c1, b1, r1)
	}
	if c2 != c1 || b2 != b1 || r2 != "true" {
		t.Fatalf("retry should replay the first response:\n%d %s\n%d %s replayed=%q", c1, b1, c2, b2, r2)
	}
	var n int
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM expenses WHERE group_id=$1 AND description='Momo'`, gid).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("expenses created = %d, want 1", n)
	}

	// Same key on a different route is a client bug, not a retry.
	code, out := doJSON(t, "POST", srv.URL+"/api/v1/groups", tok, map[string]string{"name": "Other", "currency": "NPR"}, map[string]string{"Idempotency-Key": key})
	if code != 422 || out["error"].(map[string]any)["code"] != "IDEMPOTENCY_KEY_REUSED" {
		t.Fatalf("key reuse on another route: %d %v", code, out)
	}
}
