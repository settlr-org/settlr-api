package search

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nabinkhanal00/settlr-api/internal/auth"
	"github.com/nabinkhanal00/settlr-api/internal/config"
	"github.com/nabinkhanal00/settlr-api/internal/expenses"
	"github.com/nabinkhanal00/settlr-api/internal/groups"
	"github.com/nabinkhanal00/settlr-api/internal/testutil"
)

func newSearchTestServer(t *testing.T, pool *pgxpool.Pool) (*httptest.Server, func(uuid.UUID) string) {
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
	handler := &Handler{Pool: pool}
	groupsHandler := &groups.Handler{Pool: pool}
	expHandler := &expenses.Handler{Pool: pool}
	mux := http.NewServeMux()
	authHandler.RegisterRoutes(mux)
	authMw := auth.Middleware(authSvc)
	handler.RegisterRoutes(mux, authMw)
	groupsHandler.RegisterRoutes(mux, authMw)
	expHandler.RegisterRoutes(mux, authMw)
	srv := httptest.NewServer(mux)
	tokenFor := func(uid uuid.UUID) string {
		tok, _ := auth.GenerateAccessToken(cfg, uid)
		return tok
	}
	return srv, tokenFor
}

func registerSearchHelper(t *testing.T, srv *httptest.Server, name, email string) (uuid.UUID, string) {
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

func TestIntegration_Search(t *testing.T) {
	pool := testutil.CleanDB(t)
	srv, tokenFor := newSearchTestServer(t, pool)
	defer srv.Close()

	aliceID, _ := registerSearchHelper(t, srv, "AliceSearch", "alice-search@test.local")
	aliceTok := tokenFor(aliceID)

	// Create group
	body, _ := json.Marshal(map[string]string{"name": "Searchable Group"})
	req, _ := http.NewRequest("POST", srv.URL+"/api/v1/groups", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+aliceTok)
	resp, _ := http.DefaultClient.Do(req)
	if resp.StatusCode != 201 {
		var errBody map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&errBody)
		t.Fatalf("create group %d %v", resp.StatusCode, errBody)
	}
	var g map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&g)
	resp.Body.Close()
	gid := g["id"].(string)

	// Create expense
	expBody, _ := json.Marshal(map[string]any{
		"description": "UniqueSearchExpense123",
		"amount":      5000,
		"paid_by":     aliceID.String(),
		"split_mode":  "EQUAL",
		"splits": []map[string]string{{"user_id": aliceID.String()}},
	})
	req, _ = http.NewRequest("POST", srv.URL+"/api/v1/groups/"+gid+"/expenses", bytes.NewReader(expBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+aliceTok)
	resp, _ = http.DefaultClient.Do(req)
	if resp.StatusCode != 201 {
		var errBody map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&errBody)
		t.Fatalf("create expense %d %v", resp.StatusCode, errBody)
	}
	resp.Body.Close()

	// Search for user
	req, _ = http.NewRequest("GET", srv.URL+"/api/v1/search?q=AliceSearch", nil)
	req.Header.Set("Authorization", "Bearer "+aliceTok)
	resp, _ = http.DefaultClient.Do(req)
	var searchOut map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&searchOut)
	resp.Body.Close()
	users := searchOut["users"].([]any)
	if len(users) == 0 {
		t.Fatalf("search users should find Alice")
	}

	// Search for group
	req, _ = http.NewRequest("GET", srv.URL+"/api/v1/search?q=Searchable", nil)
	req.Header.Set("Authorization", "Bearer "+aliceTok)
	resp, _ = http.DefaultClient.Do(req)
	_ = json.NewDecoder(resp.Body).Decode(&searchOut)
	resp.Body.Close()
	groups := searchOut["groups"].([]any)
	if len(groups) == 0 {
		t.Fatalf("search groups should find")
	}

	// Search for expense
	req, _ = http.NewRequest("GET", srv.URL+"/api/v1/search?q=UniqueSearchExpense123", nil)
	req.Header.Set("Authorization", "Bearer "+aliceTok)
	resp, _ = http.DefaultClient.Do(req)
	_ = json.NewDecoder(resp.Body).Decode(&searchOut)
	resp.Body.Close()
	expenses := searchOut["expenses"].([]any)
	if len(expenses) == 0 {
		t.Fatalf("search expenses should find")
	}

	// Empty query should return empty
	req, _ = http.NewRequest("GET", srv.URL+"/api/v1/search?q=", nil)
	req.Header.Set("Authorization", "Bearer "+aliceTok)
	resp, _ = http.DefaultClient.Do(req)
	_ = json.NewDecoder(resp.Body).Decode(&searchOut)
	resp.Body.Close()
	if len(searchOut["users"].([]any)) != 0 || len(searchOut["groups"].([]any)) != 0 || len(searchOut["expenses"].([]any)) != 0 {
		t.Fatalf("empty q should return empty")
	}

	_ = gid
}

var _ *pgxpool.Pool
