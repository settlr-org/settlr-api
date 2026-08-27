package settlements

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nabinkhanal00/settlr-api/internal/auth"
	"github.com/nabinkhanal00/settlr-api/internal/balances"
	"github.com/nabinkhanal00/settlr-api/internal/config"
	"github.com/nabinkhanal00/settlr-api/internal/expenses"
	"github.com/nabinkhanal00/settlr-api/internal/groups"
	"github.com/nabinkhanal00/settlr-api/internal/testutil"
)

func newSettleTestServer(t *testing.T, pool *pgxpool.Pool) (*httptest.Server, func(uuid.UUID) string) {
	t.Helper()
	cfg := config.Config{
		JWTSecret:        "test-jwt-secret-32-chars-min!!!!",
		JWTRefreshSecret: "test-refresh-secret-32-chars!!",
		JWTExpiryMinutes: 15,
		RefreshExpiryDays: 30,
		Env:              "test",
	}
	authSvc := &auth.Service{Pool: pool, Cfg: cfg}
	authHandler := &auth.Handler{Svc: authSvc}
	groupsHandler := &groups.Handler{Pool: pool}
	expHandler := &expenses.Handler{Pool: pool}
	balHandler := &balances.Handler{Pool: pool}
	settleHandler := &Handler{Pool: pool}
	mux := http.NewServeMux()
	authHandler.RegisterRoutes(mux)
	authMw := auth.Middleware(authSvc)
	groupsHandler.RegisterRoutes(mux, authMw)
	expHandler.RegisterRoutes(mux, authMw)
	balHandler.RegisterRoutes(mux, authMw)
	settleHandler.RegisterRoutes(mux, authMw)
	srv := httptest.NewServer(mux)
	tokenFor := func(uid uuid.UUID) string {
		tok, _ := auth.GenerateAccessToken(cfg, uid)
		return tok
	}
	return srv, tokenFor
}

func registerHelper(t *testing.T, srv *httptest.Server, name, email string) (uuid.UUID, string) {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"name": name, "email": email, "password": "password123"})
	resp, err := http.Post(srv.URL+"/api/v1/auth/register", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	u := out["user"].(map[string]any)
	id, _ := uuid.Parse(u["id"].(string))
	return id, out["access_token"].(string)
}

func TestIntegration_SettlementFlow(t *testing.T) {
	pool := testutil.CleanDB(t)
	srv, tokenFor := newSettleTestServer(t, pool)
	defer srv.Close()

	aliceID, _ := registerHelper(t, srv, "Alice", "alice-settle@test.local")
	bobID, _ := registerHelper(t, srv, "Bob", "bob-settle@test.local")
	aliceTok := tokenFor(aliceID)

	// Create group
	body, _ := json.Marshal(map[string]string{"name": "Settle Group"})
	req, _ := http.NewRequest("POST", srv.URL+"/api/v1/groups", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+aliceTok)
	resp, _ := http.DefaultClient.Do(req)
	var g map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&g)
	resp.Body.Close()
	gid := g["id"].(string)

	// Add Bob
	addBody, _ := json.Marshal(map[string]string{"user_id": bobID.String()})
	req, _ = http.NewRequest("POST", srv.URL+"/api/v1/groups/"+gid+"/members", bytes.NewReader(addBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+aliceTok)
	resp, _ = http.DefaultClient.Do(req)
	resp.Body.Close()

	// Create expense Alice paid 10000 split equally
	expBody, _ := json.Marshal(map[string]any{
		"description": "Dinner",
		"amount":      10000,
		"paid_by":     aliceID.String(),
		"split_mode":  "EQUAL",
		"splits": []map[string]string{{"user_id": aliceID.String()}, {"user_id": bobID.String()}},
	})
	req, _ = http.NewRequest("POST", srv.URL+"/api/v1/groups/"+gid+"/expenses", bytes.NewReader(expBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+aliceTok)
	resp, _ = http.DefaultClient.Do(req)
	resp.Body.Close()

	// Balances: Alice +5000, Bob -5000
	req, _ = http.NewRequest("GET", srv.URL+"/api/v1/groups/"+gid+"/balances", nil)
	req.Header.Set("Authorization", "Bearer "+aliceTok)
	resp, _ = http.DefaultClient.Do(req)
	var bal map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&bal)
	resp.Body.Close()
	m := map[string]int64{}
	for _, b := range bal["data"].([]any) {
		mm := b.(map[string]any)
		m[mm["user_id"].(string)] = int64(mm["amount"].(float64))
	}
	if m[aliceID.String()] != 5000 {
		t.Fatalf("alice bal %d", m[aliceID.String()])
	}

	// Settlement with invalid amount 0 should be 422
	badBody, _ := json.Marshal(map[string]any{"from_user": bobID.String(), "to_user": aliceID.String(), "amount": 0})
	req, _ = http.NewRequest("POST", srv.URL+"/api/v1/groups/"+gid+"/settlements", bytes.NewReader(badBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+aliceTok)
	resp, _ = http.DefaultClient.Do(req)
	if resp.StatusCode != 422 {
		t.Fatalf("zero amount should be 422 got %d", resp.StatusCode)
	}
	resp.Body.Close()

	// Valid settlement 5000
	goodBody, _ := json.Marshal(map[string]any{"from_user": bobID.String(), "to_user": aliceID.String(), "amount": 5000})
	req, _ = http.NewRequest("POST", srv.URL+"/api/v1/groups/"+gid+"/settlements", bytes.NewReader(goodBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+aliceTok)
	resp, _ = http.DefaultClient.Do(req)
	if resp.StatusCode != 201 {
		t.Fatalf("settlement 201 got %d", resp.StatusCode)
	}
	var sOut map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&sOut)
	resp.Body.Close()
	sid := sOut["id"].(string)

	// Balances after settlement should be 0/0
	req, _ = http.NewRequest("GET", srv.URL+"/api/v1/groups/"+gid+"/balances", nil)
	req.Header.Set("Authorization", "Bearer "+aliceTok)
	resp, _ = http.DefaultClient.Do(req)
	_ = json.NewDecoder(resp.Body).Decode(&bal)
	resp.Body.Close()
	m = map[string]int64{}
	for _, b := range bal["data"].([]any) {
		mm := b.(map[string]any)
		m[mm["user_id"].(string)] = int64(mm["amount"].(float64))
	}
	if m[aliceID.String()] != 0 || m[bobID.String()] != 0 {
		t.Fatalf("after settlement should be 0, got %v", m)
	}

	// Edit settlement to 3000 -> balances 2000/-2000
	editBody, _ := json.Marshal(map[string]any{"amount": 3000})
	req, _ = http.NewRequest("PATCH", srv.URL+"/api/v1/settlements/"+sid, bytes.NewReader(editBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+aliceTok)
	resp, _ = http.DefaultClient.Do(req)
	if resp.StatusCode != 200 {
		t.Fatalf("edit settlement %d", resp.StatusCode)
	}
	resp.Body.Close()

	req, _ = http.NewRequest("GET", srv.URL+"/api/v1/groups/"+gid+"/balances", nil)
	req.Header.Set("Authorization", "Bearer "+aliceTok)
	resp, _ = http.DefaultClient.Do(req)
	_ = json.NewDecoder(resp.Body).Decode(&bal)
	resp.Body.Close()
	m = map[string]int64{}
	for _, b := range bal["data"].([]any) {
		mm := b.(map[string]any)
		m[mm["user_id"].(string)] = int64(mm["amount"].(float64))
	}
	if m[aliceID.String()] != 2000 {
		t.Fatalf("after edit settlement alice %d want 2000", m[aliceID.String()])
	}

	// Delete settlement -> back to 5000
	req, _ = http.NewRequest("DELETE", srv.URL+"/api/v1/settlements/"+sid, nil)
	req.Header.Set("Authorization", "Bearer "+aliceTok)
	resp, _ = http.DefaultClient.Do(req)
	if resp.StatusCode != 200 {
		t.Fatalf("delete settlement %d", resp.StatusCode)
	}
	resp.Body.Close()

	req, _ = http.NewRequest("GET", srv.URL+"/api/v1/groups/"+gid+"/balances", nil)
	req.Header.Set("Authorization", "Bearer "+aliceTok)
	resp, _ = http.DefaultClient.Do(req)
	_ = json.NewDecoder(resp.Body).Decode(&bal)
	resp.Body.Close()
	m = map[string]int64{}
	for _, b := range bal["data"].([]any) {
		mm := b.(map[string]any)
		m[mm["user_id"].(string)] = int64(mm["amount"].(float64))
	}
	if m[aliceID.String()] != 5000 {
		t.Fatalf("after delete settlement alice %d want 5000", m[aliceID.String()])
	}

	// IDOR: settlement from non-member should fail (Charlie not in group)
	charlieID, _ := registerHelper(t, srv, "Charlie", "charlie-settle@test.local")
	badSettle, _ := json.Marshal(map[string]any{"from_user": charlieID.String(), "to_user": aliceID.String(), "amount": 1000})
	req, _ = http.NewRequest("POST", srv.URL+"/api/v1/groups/"+gid+"/settlements", bytes.NewReader(badSettle))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+aliceTok)
	resp, _ = http.DefaultClient.Do(req)
	if resp.StatusCode != 422 {
		t.Fatalf("non-member settlement should be 422 got %d", resp.StatusCode)
	}
	resp.Body.Close()
}
