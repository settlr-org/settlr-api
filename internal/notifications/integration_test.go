package notifications

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
	"github.com/nabinkhanal00/settlr-api/internal/friends"
	"github.com/nabinkhanal00/settlr-api/internal/testutil"
)

func newNotifTestServer(t *testing.T, pool *pgxpool.Pool) (*httptest.Server, func(uuid.UUID) string) {
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
	notifHandler := &Handler{Pool: pool}
	friendsHandler := &friends.Handler{Pool: pool}
	mux := http.NewServeMux()
	authHandler.RegisterRoutes(mux)
	authMw := auth.Middleware(authSvc)
	notifHandler.RegisterRoutes(mux, authMw)
	friendsHandler.RegisterRoutes(mux, authMw)
	srv := httptest.NewServer(mux)
	tokenFor := func(uid uuid.UUID) string {
		tok, _ := auth.GenerateAccessToken(cfg, uid)
		return tok
	}
	return srv, tokenFor
}

func registerNotifHelper(t *testing.T, srv *httptest.Server, name, email string) (uuid.UUID, string) {
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

func TestIntegration_NotificationsFlow(t *testing.T) {
	pool := testutil.CleanDB(t)
	srv, tokenFor := newNotifTestServer(t, pool)
	defer srv.Close()

	aliceID, _ := registerNotifHelper(t, srv, "AliceN", "alice-notif@test.local")
	bobID, _ := registerNotifHelper(t, srv, "BobN", "bob-notif@test.local")
	aliceTok := tokenFor(aliceID)
	bobTok := tokenFor(bobID)

	// Initially no notifications
	req, _ := http.NewRequest("GET", srv.URL+"/api/v1/notifications", nil)
	req.Header.Set("Authorization", "Bearer "+bobTok)
	resp, _ := http.DefaultClient.Do(req)
	var list map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&list)
	resp.Body.Close()
	if int(list["unread_count"].(float64)) != 0 {
		t.Fatalf("initial unread should be 0")
	}

	// Alice sends friend request to Bob -> Bob should get notification
	req, _ = http.NewRequest("POST", srv.URL+"/api/v1/friends/"+bobID.String()+"/request", nil)
	req.Header.Set("Authorization", "Bearer "+aliceTok)
	resp, _ = http.DefaultClient.Do(req)
	resp.Body.Close()
	if resp.StatusCode != 201 {
		t.Fatalf("friend request %d", resp.StatusCode)
	}

	// Bob lists notifications -> should have 1 unread
	req, _ = http.NewRequest("GET", srv.URL+"/api/v1/notifications", nil)
	req.Header.Set("Authorization", "Bearer "+bobTok)
	resp, _ = http.DefaultClient.Do(req)
	_ = json.NewDecoder(resp.Body).Decode(&list)
	resp.Body.Close()
	if int(list["unread_count"].(float64)) != 1 {
		t.Fatalf("unread should be 1 got %v", list["unread_count"])
	}
	data := list["data"].([]any)
	if len(data) != 1 {
		t.Fatalf("data len 1 got %d", len(data))
	}
	notif := data[0].(map[string]any)
	notifID := notif["id"].(string)

	// Bob marks as read
	req, _ = http.NewRequest("POST", srv.URL+"/api/v1/notifications/"+notifID+"/read", nil)
	req.Header.Set("Authorization", "Bearer "+bobTok)
	resp, _ = http.DefaultClient.Do(req)
	if resp.StatusCode != 200 {
		t.Fatalf("mark read %d", resp.StatusCode)
	}
	resp.Body.Close()

	// Unread should be 0
	req, _ = http.NewRequest("GET", srv.URL+"/api/v1/notifications", nil)
	req.Header.Set("Authorization", "Bearer "+bobTok)
	resp, _ = http.DefaultClient.Do(req)
	_ = json.NewDecoder(resp.Body).Decode(&list)
	resp.Body.Close()
	if int(list["unread_count"].(float64)) != 0 {
		t.Fatalf("unread should be 0 after read")
	}

	// Test pagination: create 35 notifications via direct DB insert for Bob, then list with limit
	// Use direct DB to create many notifications
	for i := 0; i < 35; i++ {
		_, _ = pool.Exec(t.Context(), `INSERT INTO notifications (user_id, type, title, body) VALUES ($1,'MENTION',$2,$3)`, bobID, "Test", "Body")
	}
	req, _ = http.NewRequest("GET", srv.URL+"/api/v1/notifications?limit=10", nil)
	req.Header.Set("Authorization", "Bearer "+bobTok)
	resp, _ = http.DefaultClient.Do(req)
	_ = json.NewDecoder(resp.Body).Decode(&list)
	resp.Body.Close()
	if len(list["data"].([]any)) != 10 {
		t.Fatalf("limit 10 should return 10 got %d", len(list["data"].([]any)))
	}
	if _, ok := list["next_cursor"]; !ok {
		t.Fatalf("should have next_cursor when more data")
	}

	_ = aliceID
	_ = bobID
}

// Ensure testutil is used
var _ = testutil.CleanDB
var _ *pgxpool.Pool
