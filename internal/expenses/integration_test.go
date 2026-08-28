package expenses

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nabinkhanal00/settlr-api/internal/auth"
	"github.com/nabinkhanal00/settlr-api/internal/balances"
	"github.com/nabinkhanal00/settlr-api/internal/config"
	"github.com/nabinkhanal00/settlr-api/internal/groups"
	"github.com/nabinkhanal00/settlr-api/internal/settlements"
	"github.com/nabinkhanal00/settlr-api/internal/testutil"
)

func ensureFriends(t *testing.T, pool *pgxpool.Pool, a, b uuid.UUID) {
	t.Helper()
	aa, bb := a.String(), b.String()
	if aa > bb {
		aa, bb = bb, aa
	}
	if _, err := pool.Exec(context.Background(), `INSERT INTO friendships (id, user_id, friend_id, status, action_by) VALUES (gen_random_uuid(), $1::uuid, $2::uuid, 'ACCEPTED', $1::uuid) ON CONFLICT (user_id, friend_id) DO UPDATE SET status='ACCEPTED'`, aa, bb); err != nil {
		t.Fatalf("make friends: %v", err)
	}
}

func newTestServer(t *testing.T, pool *pgxpool.Pool) (*httptest.Server, func(uuid.UUID) string) {
	t.Helper()
	cfg := config.Config{
		JWTSecret:         "test-jwt-secret-32-chars-min!!!!",
		JWTRefreshSecret:  "test-refresh-secret-32-chars!!",
		JWTExpiryMinutes:  15,
		RefreshExpiryDays: 30,
		Env:               "test",
	}
	authSvc := &auth.Service{Pool: pool, Cfg: cfg}
	authHandler := &auth.Handler{Svc: authSvc}
	groupsHandler := &groups.Handler{Pool: pool}
	expHandler := &Handler{Pool: pool}
	balHandler := &balances.Handler{Pool: pool}
	settleHandler := &settlements.Handler{Pool: pool}

	mux := http.NewServeMux()
	authHandler.RegisterRoutes(mux)
	// wrap auth routes without rate limit for tests
	authMw := auth.Middleware(authSvc)
	groupsHandler.RegisterRoutes(mux, authMw)
	expHandler.RegisterRoutes(mux, authMw)
	balHandler.RegisterRoutes(mux, authMw)
	settleHandler.RegisterRoutes(mux, authMw)

	srv := httptest.NewServer(mux)
	tokenFor := func(userID uuid.UUID) string {
		tok, err := auth.GenerateAccessToken(cfg, userID)
		if err != nil {
			t.Fatalf("token gen: %v", err)
		}
		return tok
	}
	return srv, tokenFor
}

func registerUserViaAPI(t *testing.T, srv *httptest.Server, name, email, password string) uuid.UUID {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"name": name, "email": email, "password": password})
	resp, err := http.Post(srv.URL+"/api/v1/auth/register", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 201 {
		var errBody map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&errBody)
		t.Fatalf("register status %d body %v", resp.StatusCode, errBody)
	}
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	user := out["user"].(map[string]any)
	id, _ := uuid.Parse(user["id"].(string))
	return id
}

func loginViaAPI(t *testing.T, srv *httptest.Server, email, password string) string {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"email": email, "password": password})
	resp, err := http.Post(srv.URL+"/api/v1/auth/login", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return out["access_token"].(string)
}

func TestIntegration_FullWorkflow(t *testing.T) {
	pool := testutil.CleanDB(t)
	srv, tokenFor := newTestServer(t, pool)
	defer srv.Close()

	// Create two users
	aliceID := registerUserViaAPI(t, srv, "Alice", "alice-int@test.local", "password123")
	bobID := registerUserViaAPI(t, srv, "Bob", "bob-int@test.local", "password123")
	ensureFriends(t, pool, aliceID, bobID)
	aliceTok := tokenFor(aliceID)
	_ = tokenFor(bobID) // bob's token not needed for this flow, but create

	// Alice creates group
	body, _ := json.Marshal(map[string]string{"name": "Test Group", "currency": "USD"})
	req, _ := http.NewRequest("POST", srv.URL+"/api/v1/groups", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+aliceTok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("create group: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 201 {
		var errBody map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&errBody)
		t.Fatalf("create group status %d %v", resp.StatusCode, errBody)
	}
	var groupOut map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&groupOut)
	groupIDStr := groupOut["id"].(string)
	groupID, _ := uuid.Parse(groupIDStr)

	// Alice adds Bob
	addBody, _ := json.Marshal(map[string]string{"user_id": bobID.String()})
	req, _ = http.NewRequest("POST", fmt.Sprintf("%s/api/v1/groups/%s/members", srv.URL, groupID), bytes.NewReader(addBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+aliceTok)
	resp, _ = http.DefaultClient.Do(req)
	if resp.StatusCode != 201 {
		var errBody map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&errBody)
		t.Fatalf("add member %d %v", resp.StatusCode, errBody)
	}
	resp.Body.Close()

	// Alice creates expense $100 equally split Alice/Bob
	expBody, _ := json.Marshal(map[string]any{
		"description": "Dinner",
		"amount":      10000,
		"paid_by":     aliceID.String(),
		"split_mode":  "EQUAL",
		"splits": []map[string]string{
			{"user_id": aliceID.String()},
			{"user_id": bobID.String()},
		},
	})
	req, _ = http.NewRequest("POST", fmt.Sprintf("%s/api/v1/groups/%s/expenses", srv.URL, groupID), bytes.NewReader(expBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+aliceTok)
	resp, _ = http.DefaultClient.Do(req)
	if resp.StatusCode != 201 {
		var errBody map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&errBody)
		t.Fatalf("create expense %d %v", resp.StatusCode, errBody)
	}
	var expOut map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&expOut)
	expIDStr := expOut["id"].(string)
	resp.Body.Close()

	// Check balances: Alice +5000, Bob -5000
	req, _ = http.NewRequest("GET", fmt.Sprintf("%s/api/v1/groups/%s/balances", srv.URL, groupID), nil)
	req.Header.Set("Authorization", "Bearer "+aliceTok)
	resp, _ = http.DefaultClient.Do(req)
	var balOut map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&balOut)
	resp.Body.Close()
	data := balOut["data"].([]any)
	m := make(map[string]int64)
	for _, item := range data {
		mm := item.(map[string]any)
		uid := mm["user_id"].(string)
		amt := int64(mm["amount"].(float64))
		m[uid] = amt
	}
	if m[aliceID.String()] != 5000 || m[bobID.String()] != -5000 {
		t.Fatalf("balances incorrect %v", m)
	}
	// Sum must be 0
	var sum int64
	for _, v := range m {
		sum += v
	}
	if sum != 0 {
		t.Fatalf("sum not zero %d", sum)
	}

	// Check debts: Bob -> Alice 5000
	req, _ = http.NewRequest("GET", fmt.Sprintf("%s/api/v1/groups/%s/debts", srv.URL, groupID), nil)
	req.Header.Set("Authorization", "Bearer "+aliceTok)
	resp, _ = http.DefaultClient.Do(req)
	var debtOut map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&debtOut)
	resp.Body.Close()
	debts := debtOut["data"].([]any)
	if len(debts) != 1 {
		t.Fatalf("debts len %d", len(debts))
	}
	debt := debts[0].(map[string]any)
	if debt["from_user"].(string) != bobID.String() || debt["to_user"].(string) != aliceID.String() || int64(debt["amount"].(float64)) != 5000 {
		t.Fatalf("debt incorrect %v", debt)
	}

	// Bob settles 3000 to Alice
	settleBody, _ := json.Marshal(map[string]any{
		"from_user": bobID.String(),
		"to_user":   aliceID.String(),
		"amount":    3000,
	})
	req, _ = http.NewRequest("POST", fmt.Sprintf("%s/api/v1/groups/%s/settlements", srv.URL, groupID), bytes.NewReader(settleBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+aliceTok)
	resp, _ = http.DefaultClient.Do(req)
	if resp.StatusCode != 201 {
		var errBody map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&errBody)
		t.Fatalf("settlement %d %v", resp.StatusCode, errBody)
	}
	resp.Body.Close()

	// Balances after settlement: Alice 2000, Bob -2000
	req, _ = http.NewRequest("GET", fmt.Sprintf("%s/api/v1/groups/%s/balances", srv.URL, groupID), nil)
	req.Header.Set("Authorization", "Bearer "+aliceTok)
	resp, _ = http.DefaultClient.Do(req)
	_ = json.NewDecoder(resp.Body).Decode(&balOut)
	resp.Body.Close()
	data = balOut["data"].([]any)
	m = make(map[string]int64)
	for _, item := range data {
		mm := item.(map[string]any)
		m[mm["user_id"].(string)] = int64(mm["amount"].(float64))
	}
	if m[aliceID.String()] != 2000 || m[bobID.String()] != -2000 {
		t.Fatalf("after settlement balances %v", m)
	}

	// Edit expense: change amount to $80 (8000) -> Alice 4000, Bob -4000, but settlement 3000 remains, so net Alice 1000, Bob -1000
	editBody, _ := json.Marshal(map[string]any{
		"description": "Dinner edited",
		"amount":      8000,
		"paid_by":     aliceID.String(),
		"split_mode":  "EQUAL",
		"splits": []map[string]string{
			{"user_id": aliceID.String()},
			{"user_id": bobID.String()},
		},
	})
	req, _ = http.NewRequest("PATCH", fmt.Sprintf("%s/api/v1/expenses/%s", srv.URL, expIDStr), bytes.NewReader(editBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+aliceTok)
	resp, _ = http.DefaultClient.Do(req)
	if resp.StatusCode != 200 {
		var errBody map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&errBody)
		t.Fatalf("edit %d %v", resp.StatusCode, errBody)
	}
	resp.Body.Close()

	req, _ = http.NewRequest("GET", fmt.Sprintf("%s/api/v1/groups/%s/balances", srv.URL, groupID), nil)
	req.Header.Set("Authorization", "Bearer "+aliceTok)
	resp, _ = http.DefaultClient.Do(req)
	_ = json.NewDecoder(resp.Body).Decode(&balOut)
	resp.Body.Close()
	data = balOut["data"].([]any)
	m = make(map[string]int64)
	for _, item := range data {
		mm := item.(map[string]any)
		m[mm["user_id"].(string)] = int64(mm["amount"].(float64))
	}
	if m[aliceID.String()] != 1000 || m[bobID.String()] != -1000 {
		t.Fatalf("after edit balances %v", m)
	}

	// Delete expense -> settlement remains, but expense gone, so balances should be 3000/ -3000? Actually after delete, only settlement remains: Alice -3000, Bob +3000? Wait settlement from Bob to Alice 3000: Bob +3000, Alice -3000, so after delete, Alice -3000, Bob +3000? Let's compute: settlement: Bob +3000, Alice -3000. No expense, so balances: Alice -3000, Bob +3000. But our earlier logic settlement is Bob +3000, Alice -3000, so after delete, balances should be Alice -3000, Bob +3000.
	req, _ = http.NewRequest("DELETE", fmt.Sprintf("%s/api/v1/expenses/%s", srv.URL, expIDStr), nil)
	req.Header.Set("Authorization", "Bearer "+aliceTok)
	resp, _ = http.DefaultClient.Do(req)
	if resp.StatusCode != 200 {
		t.Fatalf("delete %d", resp.StatusCode)
	}
	resp.Body.Close()

	req, _ = http.NewRequest("GET", fmt.Sprintf("%s/api/v1/groups/%s/balances", srv.URL, groupID), nil)
	req.Header.Set("Authorization", "Bearer "+aliceTok)
	resp, _ = http.DefaultClient.Do(req)
	_ = json.NewDecoder(resp.Body).Decode(&balOut)
	resp.Body.Close()
	data = balOut["data"].([]any)
	m = make(map[string]int64)
	for _, item := range data {
		mm := item.(map[string]any)
		m[mm["user_id"].(string)] = int64(mm["amount"].(float64))
	}
	if m[aliceID.String()] != -3000 || m[bobID.String()] != 3000 {
		t.Fatalf("after delete balances %v", m)
	}
	// Sum still 0
	sum = 0
	for _, v := range m {
		sum += v
	}
	if sum != 0 {
		t.Fatalf("sum not zero after delete %d", sum)
	}
	_ = groupID
}

func TestIntegration_IDOR(t *testing.T) {
	pool := testutil.CleanDB(t)
	srv, tokenFor := newTestServer(t, pool)
	defer srv.Close()

	aliceID := registerUserViaAPI(t, srv, "AliceIDOR", "alice-idor@test.local", "password123")
	bobID := registerUserViaAPI(t, srv, "BobIDOR", "bob-idor@test.local", "password123")
	aliceTok := tokenFor(aliceID)
	bobTok := tokenFor(bobID)

	// Alice creates group
	body, _ := json.Marshal(map[string]string{"name": "Alice Group"})
	req, _ := http.NewRequest("POST", srv.URL+"/api/v1/groups", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+aliceTok)
	resp, _ := http.DefaultClient.Do(req)
	var groupOut map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&groupOut)
	resp.Body.Close()
	groupID := groupOut["id"].(string)

	// Bob tries to access Alice's group -> should be 403
	req, _ = http.NewRequest("GET", srv.URL+"/api/v1/groups/"+groupID, nil)
	req.Header.Set("Authorization", "Bearer "+bobTok)
	resp, _ = http.DefaultClient.Do(req)
	if resp.StatusCode != 403 {
		t.Fatalf("IDOR: bob accessed alice group status %d want 403", resp.StatusCode)
	}
	resp.Body.Close()

	// Bob tries to list Alice group expenses -> 403
	req, _ = http.NewRequest("GET", srv.URL+"/api/v1/groups/"+groupID+"/expenses", nil)
	req.Header.Set("Authorization", "Bearer "+bobTok)
	resp, _ = http.DefaultClient.Do(req)
	if resp.StatusCode != 403 {
		t.Fatalf("IDOR expenses %d", resp.StatusCode)
	}
	resp.Body.Close()

	_ = bobID
}

func TestIntegration_InvalidSplit(t *testing.T) {
	pool := testutil.CleanDB(t)
	srv, tokenFor := newTestServer(t, pool)
	defer srv.Close()

	aliceID := registerUserViaAPI(t, srv, "AliceInv", "alice-inv@test.local", "password123")
	aliceTok := tokenFor(aliceID)

	body, _ := json.Marshal(map[string]string{"name": "Inv Group"})
	req, _ := http.NewRequest("POST", srv.URL+"/api/v1/groups", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+aliceTok)
	resp, _ := http.DefaultClient.Do(req)
	var groupOut map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&groupOut)
	resp.Body.Close()
	groupID := groupOut["id"].(string)

	// Try EXACT split that doesn't sum to amount
	expBody, _ := json.Marshal(map[string]any{
		"description": "Bad",
		"amount":      10000,
		"paid_by":     aliceID.String(),
		"split_mode":  "EXACT",
		"splits": []map[string]any{
			{"user_id": aliceID.String(), "amount": 5000},
			{"user_id": aliceID.String(), "amount": 4000}, // sum 9000 != 10000
		},
	})
	req, _ = http.NewRequest("POST", srv.URL+"/api/v1/groups/"+groupID+"/expenses", bytes.NewReader(expBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+aliceTok)
	resp, _ = http.DefaultClient.Do(req)
	if resp.StatusCode != 422 {
		t.Fatalf("invalid split should be 422 got %d", resp.StatusCode)
	}
	resp.Body.Close()

	// Transaction atomicity: ensure no expense was created
	req, _ = http.NewRequest("GET", srv.URL+"/api/v1/groups/"+groupID+"/expenses", nil)
	req.Header.Set("Authorization", "Bearer "+aliceTok)
	resp, _ = http.DefaultClient.Do(req)
	var listOut map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&listOut)
	resp.Body.Close()
	if data, ok := listOut["data"].([]any); ok && len(data) != 0 {
		t.Fatalf("expected 0 expenses after failed create, got %d", len(data))
	}
}

func TestIntegration_UpdateExpense_NonMemberRejected(t *testing.T) {
	pool := testutil.CleanDB(t)
	srv, tokenFor := newTestServer(t, pool)
	defer srv.Close()

	aliceID := registerUserViaAPI(t, srv, "AliceUpd", "alice-upd@test.local", "password123")
	bobID := registerUserViaAPI(t, srv, "BobUpd", "bob-upd@test.local", "password123")
	charlieID := registerUserViaAPI(t, srv, "CharlieUpd", "charlie-upd@test.local", "password123")
	ensureFriends(t, pool, aliceID, bobID)
	aliceTok := tokenFor(aliceID)

	// Alice creates group with Bob
	body, _ := json.Marshal(map[string]string{"name": "Upd Group"})
	req, _ := http.NewRequest("POST", srv.URL+"/api/v1/groups", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+aliceTok)
	resp, _ := http.DefaultClient.Do(req)
	var groupOut map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&groupOut)
	resp.Body.Close()
	groupID := groupOut["id"].(string)

	addBody, _ := json.Marshal(map[string]string{"user_id": bobID.String()})
	req, _ = http.NewRequest("POST", srv.URL+"/api/v1/groups/"+groupID+"/members", bytes.NewReader(addBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+aliceTok)
	resp, _ = http.DefaultClient.Do(req)
	resp.Body.Close()

	// Create valid expense
	expBody, _ := json.Marshal(map[string]any{
		"description": "Valid",
		"amount":      10000,
		"paid_by":     aliceID.String(),
		"split_mode":  "EQUAL",
		"splits":      []map[string]string{{"user_id": aliceID.String()}, {"user_id": bobID.String()}},
	})
	req, _ = http.NewRequest("POST", srv.URL+"/api/v1/groups/"+groupID+"/expenses", bytes.NewReader(expBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+aliceTok)
	resp, _ = http.DefaultClient.Do(req)
	var expOut map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&expOut)
	resp.Body.Close()
	expID := expOut["id"].(string)

	// Try to update with Charlie (non-member) as participant -> should be 422
	editBody, _ := json.Marshal(map[string]any{
		"description": "Hacked",
		"amount":      10000,
		"paid_by":     aliceID.String(),
		"split_mode":  "EQUAL",
		"splits":      []map[string]string{{"user_id": aliceID.String()}, {"user_id": charlieID.String()}},
	})
	req, _ = http.NewRequest("PATCH", srv.URL+"/api/v1/expenses/"+expID, bytes.NewReader(editBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+aliceTok)
	resp, _ = http.DefaultClient.Do(req)
	if resp.StatusCode != 422 {
		t.Fatalf("non-member update should be 422 got %d", resp.StatusCode)
	}
	resp.Body.Close()

	// Try to update with Charlie as payer -> should be 422
	editBody2, _ := json.Marshal(map[string]any{
		"description": "Hacked Payer",
		"amount":      10000,
		"paid_by":     charlieID.String(),
		"split_mode":  "EQUAL",
		"splits":      []map[string]string{{"user_id": aliceID.String()}, {"user_id": bobID.String()}},
	})
	req, _ = http.NewRequest("PATCH", srv.URL+"/api/v1/expenses/"+expID, bytes.NewReader(editBody2))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+aliceTok)
	resp, _ = http.DefaultClient.Do(req)
	if resp.StatusCode != 422 {
		t.Fatalf("non-member payer should be 422 got %d", resp.StatusCode)
	}
	resp.Body.Close()

	_ = charlieID
}
