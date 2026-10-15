package auth

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/settlr-org/settlr-api/internal/config"
	"github.com/settlr-org/settlr-api/internal/db"
	"github.com/settlr-org/settlr-api/internal/testutil"
)

// Refresh rotates the session row; the list must show one live session that keeps its device and
// original sign-in time, and stale history must be prunable.
func TestSessionRotationListAndPrune(t *testing.T) {
	pool := testutil.CleanDB(t)
	ctx := t.Context()
	q := db.New(pool)
	userID := uuid.New()
	if _, err := pool.Exec(ctx, `INSERT INTO users (id, name, email, password_hash, email_verified_at) VALUES ($1, 'Sam', 'sam-sessions@test.local', 'x', now())`, userID); err != nil {
		t.Fatal(err)
	}
	svc := &Service{Pool: pool, Queries: q}
	if _, err := q.CreateSession(ctx, db.CreateSessionParams{
		UserID: userID, RefreshTokenHash: HashToken("r0"),
		UserAgent: pgtype.Text{String: "Settlr Android", Valid: true}, Ip: pgtype.Text{String: "10.0.0.1", Valid: true},
		ExpiresAt: pgtype.Timestamptz{Time: time.Now().Add(30 * 24 * time.Hour), Valid: true},
	}); err != nil {
		t.Fatal(err)
	}
	var signedIn time.Time
	if err := pool.QueryRow(ctx, `UPDATE sessions SET created_at = now() - interval '3 days' WHERE user_id = $1 RETURNING created_at`, userID).Scan(&signedIn); err != nil {
		t.Fatal(err)
	}
	for i, pair := range [][2]string{{"r0", "r1"}, {"r1", "r2"}, {"r2", "r3"}} {
		if _, err := svc.RotateSession(ctx, pair[0], pair[1]); err != nil {
			t.Fatalf("rotate %d: %v", i, err)
		}
	}

	live, err := q.ListSessionsByUser(ctx, userID)
	if err != nil {
		t.Fatal(err)
	}
	if len(live) != 1 {
		t.Fatalf("want 1 live session after 3 refreshes, got %d", len(live))
	}
	s := live[0]
	if s.UserAgent.String != "Settlr Android" || s.Ip.String != "10.0.0.1" {
		t.Fatalf("rotation should keep the device, got %q %q", s.UserAgent.String, s.Ip.String)
	}
	if !s.CreatedAt.Time.Equal(signedIn) {
		t.Fatalf("rotation should keep the sign-in time %v, got %v", signedIn, s.CreatedAt.Time)
	}

	// Recent rotated rows are kept (reuse detection); old ones are pruned, the live one never is.
	if n, err := q.DeleteStaleSessions(ctx); err != nil || n != 0 {
		t.Fatalf("nothing stale yet: n=%d err=%v", n, err)
	}
	if _, err := pool.Exec(ctx, `UPDATE sessions SET revoked_at = now() - interval '31 days' WHERE user_id = $1 AND revoked_at IS NOT NULL`, userID); err != nil {
		t.Fatal(err)
	}
	if n, err := q.DeleteStaleSessions(ctx); err != nil || n != 3 {
		t.Fatalf("want 3 pruned, got n=%d err=%v", n, err)
	}
	if live, _ := q.ListSessionsByUser(ctx, userID); len(live) != 1 {
		t.Fatalf("live session must survive pruning, got %d", len(live))
	}
}

func authedJSON(t *testing.T, method, url, token string, body any) jsonResponse {
	t.Helper()
	var rd *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	} else {
		rd = bytes.NewReader(nil)
	}
	req, _ := http.NewRequest(method, url, rd)
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return jsonResponse{status: resp.StatusCode, body: out}
}

// The session list marks the caller's own session: sid rides in the access token, follows each
// refresh to the new row, is absent from older tokens, and never lets one session act for another.
func TestSessionListMarksCurrentSession(t *testing.T) {
	pool := testutil.CleanDB(t)
	svc := &Service{Pool: pool, Cfg: config.Config{
		JWTSecret: "test-jwt-secret-32-chars-min!!!!", JWTRefreshSecret: "test-refresh-secret-32-chars!!",
		JWTExpiryMinutes: 15, RefreshExpiryDays: 30, Env: "test",
	}}
	mux := http.NewServeMux()
	(&Handler{Svc: svc}).RegisterRoutes(mux)
	srv := httptest.NewServer(mux)
	defer srv.Close()
	base := srv.URL + "/api/v1/auth"

	reg := postJSON(t, base+"/register", map[string]string{"name": "Sam", "email": "sam-current@test.local", "password": "password123"})
	if reg.status != 201 {
		t.Fatalf("register %d %v", reg.status, reg.body)
	}
	if _, err := pool.Exec(t.Context(), `UPDATE users SET email_verified_at = now() WHERE email = 'sam-current@test.local'`); err != nil {
		t.Fatal(err)
	}
	// Session A comes from register (test env), session B from login.
	tokA, refA := reg.body["access_token"].(string), reg.body["refresh_token"].(string)
	login := postJSON(t, base+"/login", map[string]string{"email": "sam-current@test.local", "password": "password123"})
	tokB, refB := login.body["access_token"].(string), login.body["refresh_token"].(string)

	current := func(tok string) (id string, others int, total int) {
		t.Helper()
		res := authedJSON(t, "GET", base+"/sessions", tok, nil)
		if res.status != 200 {
			t.Fatalf("list %d %v", res.status, res.body)
		}
		for _, r := range res.body["data"].([]any) {
			row := r.(map[string]any)
			c, ok := row["current"].(bool)
			if !ok {
				t.Fatalf("row without current: %v", row)
			}
			total++
			if c {
				id = row["id"].(string)
			} else {
				others++
			}
		}
		return
	}
	idA, oth, tot := current(tokA)
	idB, _, _ := current(tokB)
	if tot != 2 || oth != 1 || idA == "" || idB == "" || idA == idB {
		t.Fatalf("two sessions, one current each: A=%s B=%s total=%d", idA, idB, tot)
	}

	// After a refresh the new token marks the rotated row; the pre-refresh token marks nothing.
	ref := postJSON(t, base+"/refresh", map[string]string{"refresh_token": refA})
	tokA2 := ref.body["access_token"].(string)
	idA2, _, tot := current(tokA2)
	if idA2 == "" || idA2 == idA || idA2 == idB || tot != 2 {
		t.Fatalf("after refresh: %s (old %s, other %s) total %d", idA2, idA, idB, tot)
	}
	if id, _, _ := current(tokA); id != "" {
		t.Fatalf("a token from before the refresh must not mark a session: %s", id)
	}
	// Older tokens without sid: current=false everywhere.
	legacy, _ := GenerateAccessToken(svc.Cfg, uuid.MustParse(reg.body["user"].(map[string]any)["id"].(string)))
	if id, _, tot := current(legacy); id != "" || tot != 2 {
		t.Fatalf("legacy token: %q total %d", id, tot)
	}

	// Revoking another session works and leaves the caller's marker alone; the token's sid
	// grants nothing by itself (revocation is by path id, scoped to the user).
	if res := authedJSON(t, "DELETE", base+"/sessions/"+idB, tokA2, nil); res.status != 200 {
		t.Fatalf("revoke other: %d %v", res.status, res.body)
	}
	if r := postJSON(t, base+"/refresh", map[string]string{"refresh_token": refB}); r.status != 401 {
		t.Fatalf("revoked session still refreshes: %d", r.status)
	}
	if id, _, tot := current(tokA2); id != idA2 || tot != 1 {
		t.Fatalf("after revoking B: current %s total %d", id, tot)
	}
	// Revoking the current session still works.
	if res := authedJSON(t, "DELETE", base+"/sessions/"+idA2, tokA2, nil); res.status != 200 {
		t.Fatalf("revoke current: %d %v", res.status, res.body)
	}
	if _, _, tot := current(tokA2); tot != 0 {
		t.Fatalf("no live sessions expected, got %d", tot)
	}
	_ = tokB
}
