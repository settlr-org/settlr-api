package friends

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
	"github.com/nabinkhanal00/settlr-api/internal/testutil"
	"github.com/nabinkhanal00/settlr-api/internal/users"
)

func newFriendsTestServer(t *testing.T, pool *pgxpool.Pool) (*httptest.Server, func(uuid.UUID) string) {
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
	handler := &Handler{Pool: pool}
	usersHandler := &users.Handler{Pool: pool, AuthSvc: authSvc}
	mux := http.NewServeMux()
	authHandler.RegisterRoutes(mux)
	authMw := auth.Middleware(authSvc)
	handler.RegisterRoutes(mux, authMw)
	usersHandler.RegisterRoutes(mux, authMw)
	srv := httptest.NewServer(mux)
	tokenFor := func(uid uuid.UUID) string {
		tok, _ := auth.GenerateAccessToken(cfg, uid)
		return tok
	}
	return srv, tokenFor
}

func registerHelperFriends(t *testing.T, srv *httptest.Server, name, email string) (uuid.UUID, string) {
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

func TestIntegration_FriendsFlow(t *testing.T) {
	pool := testutil.CleanDB(t)
	srv, tokenFor := newFriendsTestServer(t, pool)
	defer srv.Close()

	aliceID, _ := registerHelperFriends(t, srv, "AliceF", "alice-friends@test.local")
	bobID, _ := registerHelperFriends(t, srv, "BobF", "bob-friends@test.local")
	aliceTok := tokenFor(aliceID)
	bobTok := tokenFor(bobID)

	// Search includes the email and reports an outgoing request as requested.
	req, _ := http.NewRequest("GET", srv.URL+"/api/v1/users/search?q=bob-friends", nil)
	req.Header.Set("Authorization", "Bearer "+aliceTok)
	resp, _ := http.DefaultClient.Do(req)
	var search map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&search)
	resp.Body.Close()
	result := search["data"].([]any)[0].(map[string]any)
	if result["email"] != "bob-friends@test.local" || result["requested"] != false {
		t.Fatalf("unexpected search result: %#v", result)
	}

	// Alice sends friend request to Bob
	req, _ = http.NewRequest("POST", srv.URL+"/api/v1/friends/"+bobID.String()+"/request", nil)
	req.Header.Set("Authorization", "Bearer "+aliceTok)
	resp, _ = http.DefaultClient.Do(req)
	if resp.StatusCode != 201 {
		t.Fatalf("send request %d", resp.StatusCode)
	}
	resp.Body.Close()

	req, _ = http.NewRequest("GET", srv.URL+"/api/v1/users/search?q=bob-friends", nil)
	req.Header.Set("Authorization", "Bearer "+aliceTok)
	resp, _ = http.DefaultClient.Do(req)
	search = map[string]any{}
	_ = json.NewDecoder(resp.Body).Decode(&search)
	resp.Body.Close()
	result = search["data"].([]any)[0].(map[string]any)
	if result["requested"] != true {
		t.Fatalf("search should report outgoing request: %#v", result)
	}

	// Duplicate request should be 409
	req, _ = http.NewRequest("POST", srv.URL+"/api/v1/friends/"+bobID.String()+"/request", nil)
	req.Header.Set("Authorization", "Bearer "+aliceTok)
	resp, _ = http.DefaultClient.Do(req)
	if resp.StatusCode != 409 {
		t.Fatalf("duplicate should be 409 got %d", resp.StatusCode)
	}
	resp.Body.Close()

	// Bob lists requests
	req, _ = http.NewRequest("GET", srv.URL+"/api/v1/friends/requests", nil)
	req.Header.Set("Authorization", "Bearer "+bobTok)
	resp, _ = http.DefaultClient.Do(req)
	var reqList map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&reqList)
	resp.Body.Close()
	if len(reqList["data"].([]any)) != 1 {
		t.Fatalf("bob should have 1 request")
	}

	// Bob accepts
	req, _ = http.NewRequest("POST", srv.URL+"/api/v1/friends/"+aliceID.String()+"/accept", nil)
	req.Header.Set("Authorization", "Bearer "+bobTok)
	resp, _ = http.DefaultClient.Do(req)
	if resp.StatusCode != 200 {
		t.Fatalf("accept %d", resp.StatusCode)
	}
	resp.Body.Close()

	// Both should see each other in friends list
	req, _ = http.NewRequest("GET", srv.URL+"/api/v1/friends", nil)
	req.Header.Set("Authorization", "Bearer "+aliceTok)
	resp, _ = http.DefaultClient.Do(req)
	var friends map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&friends)
	resp.Body.Close()
	if len(friends["data"].([]any)) != 1 {
		t.Fatalf("alice friends len %d", len(friends["data"].([]any)))
	}

	// Alice cannot friend herself
	req, _ = http.NewRequest("POST", srv.URL+"/api/v1/friends/"+aliceID.String()+"/request", nil)
	req.Header.Set("Authorization", "Bearer "+aliceTok)
	resp, _ = http.DefaultClient.Do(req)
	if resp.StatusCode != 422 {
		t.Fatalf("self friend should be 422 got %d", resp.StatusCode)
	}
	resp.Body.Close()

	// Bob blocks Alice
	req, _ = http.NewRequest("POST", srv.URL+"/api/v1/friends/"+aliceID.String()+"/block", nil)
	req.Header.Set("Authorization", "Bearer "+bobTok)
	resp, _ = http.DefaultClient.Do(req)
	if resp.StatusCode != 200 {
		t.Fatalf("block %d", resp.StatusCode)
	}
	resp.Body.Close()

	// Alice tries to send request to blocked Bob should be 403
	req, _ = http.NewRequest("POST", srv.URL+"/api/v1/friends/"+bobID.String()+"/request", nil)
	req.Header.Set("Authorization", "Bearer "+aliceTok)
	resp, _ = http.DefaultClient.Do(req)
	if resp.StatusCode != 403 {
		t.Fatalf("blocked should be 403 got %d", resp.StatusCode)
	}
	resp.Body.Close()

	_ = bobID
}
