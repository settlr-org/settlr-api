package groups

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/settlr-org/settlr-api/internal/auth"
	"github.com/settlr-org/settlr-api/internal/config"
	"github.com/settlr-org/settlr-api/internal/testutil"
)

func newGroupTestServer(t *testing.T, pool *pgxpool.Pool) (*httptest.Server, func(uuid.UUID) string) {
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
	mux := http.NewServeMux()
	authHandler.RegisterRoutes(mux)
	authMw := auth.Middleware(authSvc)
	handler.RegisterRoutes(mux, authMw)
	srv := httptest.NewServer(mux)
	tokenFor := func(uid uuid.UUID) string {
		tok, _ := auth.GenerateAccessToken(cfg, uid)
		return tok
	}
	return srv, tokenFor
}

func registerViaAPI(t *testing.T, srv *httptest.Server, name, email string) (uuid.UUID, string) {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"name": name, "email": email, "password": "password123"})
	resp, err := http.Post(srv.URL+"/api/v1/auth/register", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 201 {
		var m map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&m)
		t.Fatalf("register %d %v", resp.StatusCode, m)
	}
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	u := out["user"].(map[string]any)
	id, _ := uuid.Parse(u["id"].(string))
	return id, out["access_token"].(string)
}

func TestIntegration_GroupCreateAndMemberFlow(t *testing.T) {
	pool := testutil.CleanDB(t)
	srv, tokenFor := newGroupTestServer(t, pool)
	defer srv.Close()

	aliceID, aliceTok := registerViaAPI(t, srv, "Alice", "alice-group@test.local")
	bobID, _ := registerViaAPI(t, srv, "Bob", "bob-group@test.local")
	_ = tokenFor(bobID) // ensure bob exists
	if _, err := pool.Exec(t.Context(), `INSERT INTO friendships (user_id, friend_id, status, action_by) VALUES (LEAST($1::uuid,$2::uuid), GREATEST($1::uuid,$2::uuid), 'ACCEPTED', $1)`, aliceID, bobID); err != nil {
		t.Fatalf("make test users friends: %v", err)
	}

	// Create group
	body, _ := json.Marshal(map[string]string{"name": "Test Group"})
	req, _ := http.NewRequest("POST", srv.URL+"/api/v1/groups", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+aliceTok)
	resp, _ := http.DefaultClient.Do(req)
	if resp.StatusCode != 201 {
		t.Fatalf("create group %d", resp.StatusCode)
	}
	var g map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&g)
	resp.Body.Close()
	gid := g["id"].(string)

	// List groups as Alice should contain it
	req, _ = http.NewRequest("GET", srv.URL+"/api/v1/groups", nil)
	req.Header.Set("Authorization", "Bearer "+aliceTok)
	resp, _ = http.DefaultClient.Do(req)
	var list map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&list)
	resp.Body.Close()
	found := false
	for _, item := range list["data"].([]any) {
		if item.(map[string]any)["id"] == gid {
			found = true
		}
	}
	if !found {
		t.Fatalf("group not in list")
	}

	// Invite Bob and accept the invite once.
	inviteBody, _ := json.Marshal(map[string]string{"user_id": bobID.String()})
	req, _ = http.NewRequest("POST", srv.URL+"/api/v1/groups/"+gid+"/invites", bytes.NewReader(inviteBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+aliceTok)
	resp, _ = http.DefaultClient.Do(req)
	if resp.StatusCode != 201 {
		t.Fatalf("create invite %d", resp.StatusCode)
	}
	var invite map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&invite)
	resp.Body.Close()
	token := invite["token"].(string)
	// The invite itself is the authorization to join. If an old friendship row
	// disappears before redemption, accepting still restores the direct friend
	// relationship and adds the invitee to the group atomically.
	if _, err := pool.Exec(t.Context(), `DELETE FROM friendships WHERE LEAST(user_id, friend_id)=LEAST($1::uuid,$2::uuid) AND GREATEST(user_id, friend_id)=GREATEST($1::uuid,$2::uuid)`, aliceID, bobID); err != nil {
		t.Fatalf("remove friendship before invite acceptance: %v", err)
	}
	req, _ = http.NewRequest("POST", srv.URL+"/api/v1/invites/"+token+"/accept", nil)
	req.Header.Set("Authorization", "Bearer "+tokenFor(bobID))
	resp, _ = http.DefaultClient.Do(req)
	if resp.StatusCode != 200 {
		t.Fatalf("accept invite %d", resp.StatusCode)
	}
	resp.Body.Close()
	var friends bool
	if err := pool.QueryRow(t.Context(), `SELECT EXISTS(SELECT 1 FROM friendships WHERE LEAST(user_id, friend_id)=LEAST($1::uuid,$2::uuid) AND GREATEST(user_id, friend_id)=GREATEST($1::uuid,$2::uuid) AND status='ACCEPTED')`, aliceID, bobID).Scan(&friends); err != nil || !friends {
		t.Fatalf("invite acceptance should establish an accepted friendship: %v", err)
	}
	// A second submission cannot duplicate membership or activity.
	req, _ = http.NewRequest("POST", srv.URL+"/api/v1/invites/"+token+"/accept", nil)
	req.Header.Set("Authorization", "Bearer "+tokenFor(bobID))
	resp, _ = http.DefaultClient.Do(req)
	if resp.StatusCode != 409 {
		t.Fatalf("duplicate invite acceptance should be 409, got %d", resp.StatusCode)
	}
	resp.Body.Close()

	// Verify Bob can now access group
	bobTok := tokenFor(bobID)
	req, _ = http.NewRequest("GET", srv.URL+"/api/v1/groups/"+gid, nil)
	req.Header.Set("Authorization", "Bearer "+bobTok)
	resp, _ = http.DefaultClient.Do(req)
	if resp.StatusCode != 200 {
		t.Fatalf("bob should access group after add, got %d", resp.StatusCode)
	}
	resp.Body.Close()

	// Update group name as Alice (owner)
	updBody, _ := json.Marshal(map[string]string{"name": "Renamed"})
	req, _ = http.NewRequest("PATCH", srv.URL+"/api/v1/groups/"+gid, bytes.NewReader(updBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+aliceTok)
	resp, _ = http.DefaultClient.Do(req)
	if resp.StatusCode != 200 {
		t.Fatalf("update group %d", resp.StatusCode)
	}
	resp.Body.Close()

	// Bob (member) should not be able to delete group (owner only)
	req, _ = http.NewRequest("DELETE", srv.URL+"/api/v1/groups/"+gid, nil)
	req.Header.Set("Authorization", "Bearer "+bobTok)
	resp, _ = http.DefaultClient.Do(req)
	if resp.StatusCode != 403 {
		t.Fatalf("bob delete should be 403 got %d", resp.StatusCode)
	}
	resp.Body.Close()

	// Alice deletes group (owner)
	req, _ = http.NewRequest("DELETE", srv.URL+"/api/v1/groups/"+gid, nil)
	req.Header.Set("Authorization", "Bearer "+aliceTok)
	resp, _ = http.DefaultClient.Do(req)
	if resp.StatusCode != 200 {
		t.Fatalf("owner delete %d", resp.StatusCode)
	}
	resp.Body.Close()

	// Verify group gone
	req, _ = http.NewRequest("GET", srv.URL+"/api/v1/groups/"+gid, nil)
	req.Header.Set("Authorization", "Bearer "+aliceTok)
	resp, _ = http.DefaultClient.Do(req)
	if resp.StatusCode != 403 && resp.StatusCode != 404 {
		t.Fatalf("deleted group should be 403 or 404 got %d", resp.StatusCode)
	}
	resp.Body.Close()

	_ = aliceID
}
