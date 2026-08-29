package comments

import (
	"bytes"
	"context"
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

func ensureFriendsComments(t *testing.T, pool *pgxpool.Pool, a, b uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	aa, bb := a.String(), b.String()
	_, _ = pool.Exec(ctx, `DELETE FROM friendships WHERE (user_id=$1::uuid AND friend_id=$2::uuid) OR (user_id=$2::uuid AND friend_id=$1::uuid)`, aa, bb)
	if _, err := pool.Exec(ctx, `INSERT INTO friendships (id, user_id, friend_id, status, action_by) VALUES (gen_random_uuid(), LEAST($1::uuid,$2::uuid), GREATEST($1::uuid,$2::uuid), 'ACCEPTED', $1::uuid)`, aa, bb); err != nil {
		t.Fatalf("make friends: %v", err)
	}
}

func newCommentsTestServer(t *testing.T, pool *pgxpool.Pool) (*httptest.Server, func(uuid.UUID) string) {
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
	expHandler := &expenses.Handler{Pool: pool}
	handler := &Handler{Pool: pool}
	mux := http.NewServeMux()
	authHandler.RegisterRoutes(mux)
	authMw := auth.Middleware(authSvc)
	groupsHandler.RegisterRoutes(mux, authMw)
	expHandler.RegisterRoutes(mux, authMw)
	handler.RegisterRoutes(mux, authMw)
	srv := httptest.NewServer(mux)
	tokenFor := func(uid uuid.UUID) string {
		tok, _ := auth.GenerateAccessToken(cfg, uid)
		return tok
	}
	return srv, tokenFor
}

func registerCommentsHelper(t *testing.T, srv *httptest.Server, name, email string) (uuid.UUID, string) {
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

func TestIntegration_CommentsFlow(t *testing.T) {
	pool := testutil.CleanDB(t)
	srv, tokenFor := newCommentsTestServer(t, pool)
	defer srv.Close()

	aliceID, _ := registerCommentsHelper(t, srv, "AliceC", "alice-comments@test.local")
	bobID, _ := registerCommentsHelper(t, srv, "BobC", "bob-comments@test.local")
	ensureFriendsComments(t, pool, aliceID, bobID)
	aliceTok := tokenFor(aliceID)
	bobTok := tokenFor(bobID)

	// Alice creates group and adds Bob
	body, _ := json.Marshal(map[string]string{"name": "Comments Group"})
	req, _ := http.NewRequest("POST", srv.URL+"/api/v1/groups", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+aliceTok)
	resp, _ := http.DefaultClient.Do(req)
	var g map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&g)
	resp.Body.Close()
	gid := g["id"].(string)

	addBody, _ := json.Marshal(map[string]string{"user_id": bobID.String()})
	req, _ = http.NewRequest("POST", srv.URL+"/api/v1/groups/"+gid+"/members", bytes.NewReader(addBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+aliceTok)
	resp, _ = http.DefaultClient.Do(req)
	resp.Body.Close()

	// Alice creates expense
	expBody, _ := json.Marshal(map[string]any{
		"description": "Test Expense",
		"amount":      10000,
		"paid_by":     aliceID.String(),
		"split_mode":  "EQUAL",
		"splits":      []map[string]string{{"user_id": aliceID.String()}, {"user_id": bobID.String()}},
	})
	req, _ = http.NewRequest("POST", srv.URL+"/api/v1/groups/"+gid+"/expenses", bytes.NewReader(expBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+aliceTok)
	resp, _ = http.DefaultClient.Do(req)
	var expOut map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&expOut)
	resp.Body.Close()
	expID := expOut["id"].(string)

	// Bob adds comment
	commentBody, _ := json.Marshal(map[string]string{"body": "Looks good!"})
	req, _ = http.NewRequest("POST", srv.URL+"/api/v1/expenses/"+expID+"/comments", bytes.NewReader(commentBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+bobTok)
	resp, _ = http.DefaultClient.Do(req)
	if resp.StatusCode != 201 {
		t.Fatalf("add comment %d", resp.StatusCode)
	}
	var commentOut map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&commentOut)
	resp.Body.Close()
	commentID := commentOut["id"].(string)

	// List comments
	req, _ = http.NewRequest("GET", srv.URL+"/api/v1/expenses/"+expID+"/comments", nil)
	req.Header.Set("Authorization", "Bearer "+aliceTok)
	resp, _ = http.DefaultClient.Do(req)
	var list map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&list)
	resp.Body.Close()
	if len(list["data"].([]any)) != 1 {
		t.Fatalf("should have 1 comment")
	}

	// Bob deletes his comment
	req, _ = http.NewRequest("DELETE", srv.URL+"/api/v1/comments/"+commentID, nil)
	req.Header.Set("Authorization", "Bearer "+bobTok)
	resp, _ = http.DefaultClient.Do(req)
	if resp.StatusCode != 200 {
		t.Fatalf("delete comment %d", resp.StatusCode)
	}
	resp.Body.Close()

	// List again should be 0
	req, _ = http.NewRequest("GET", srv.URL+"/api/v1/expenses/"+expID+"/comments", nil)
	req.Header.Set("Authorization", "Bearer "+aliceTok)
	resp, _ = http.DefaultClient.Do(req)
	_ = json.NewDecoder(resp.Body).Decode(&list)
	resp.Body.Close()
	if len(list["data"].([]any)) != 0 {
		t.Fatalf("should have 0 comments after delete")
	}

	// Alice tries to delete Bob's comment (already deleted, but even if not, should be 403)
	// Create another comment by Bob
	req, _ = http.NewRequest("POST", srv.URL+"/api/v1/expenses/"+expID+"/comments", bytes.NewReader(commentBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+bobTok)
	resp, _ = http.DefaultClient.Do(req)
	_ = json.NewDecoder(resp.Body).Decode(&commentOut)
	resp.Body.Close()
	newCommentID := commentOut["id"].(string)

	req, _ = http.NewRequest("DELETE", srv.URL+"/api/v1/comments/"+newCommentID, nil)
	req.Header.Set("Authorization", "Bearer "+aliceTok)
	resp, _ = http.DefaultClient.Do(req)
	if resp.StatusCode != 403 {
		t.Fatalf("alice delete bob comment should be 403 got %d", resp.StatusCode)
	}
	resp.Body.Close()

	_ = bobID
}

var _ *pgxpool.Pool
