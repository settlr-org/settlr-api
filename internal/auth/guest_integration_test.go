package auth

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/settlr-org/settlr-api/internal/config"
	"github.com/settlr-org/settlr-api/internal/httpx"
	"github.com/settlr-org/settlr-api/internal/testutil"
)

func TestGuestLifecycle(t *testing.T) {
	pool := testutil.CleanDB(t)
	svc := &Service{Pool: pool, Cfg: config.Config{
		JWTSecret: "test-jwt-secret-32-chars-min!!!!", JWTRefreshSecret: "test-refresh-secret-32-chars!!",
		JWTExpiryMinutes: 15, RefreshExpiryDays: 30, Env: "development",
	}}
	h := &Handler{Svc: svc}
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { httpx.WriteJSON(w, 200, map[string]any{"ok": true}) })
	mux.Handle("GET /api/v1/groups", Middleware(svc)(ok))
	mux.Handle("GET /api/v1/accounts", Middleware(svc)(ok))
	server := httptest.NewServer(BlockGuests(svc, mux))
	defer server.Close()

	do := func(method, path, token string, body any) jsonResponse {
		t.Helper()
		var rd *bytes.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			rd = bytes.NewReader(b)
		} else {
			rd = bytes.NewReader(nil)
		}
		req, _ := http.NewRequest(method, server.URL+path, rd)
		req.Header.Set("Content-Type", "application/json")
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		var out map[string]any
		_ = json.NewDecoder(res.Body).Decode(&out)
		return jsonResponse{status: res.StatusCode, body: out}
	}

	g := do("POST", "/api/v1/auth/guest", "", map[string]any{"currency": "npr"})
	if g.status != 201 || g.body["access_token"] == nil {
		t.Fatalf("create guest: %d %#v", g.status, g.body)
	}
	if u := g.body["user"].(map[string]any); u["is_guest"] != true {
		t.Fatalf("user should be a guest: %#v", u)
	}
	token := g.body["access_token"].(string)
	if bad := do("POST", "/api/v1/auth/guest", "", map[string]any{"currency": "ZZZ"}); bad.status != 422 {
		t.Fatalf("bad currency = %d", bad.status)
	}

	if r := do("GET", "/api/v1/accounts", token, nil); r.status != 200 {
		t.Fatalf("guest wallet route = %d", r.status)
	}
	if r := do("GET", "/api/v1/groups", token, nil); r.status != 403 || r.body["error"].(map[string]any)["code"] != "GUEST_NOT_ALLOWED" {
		t.Fatalf("guest group route = %d %#v", r.status, r.body)
	}
	if r := do("GET", "/api/v1/groups", "", nil); r.status != 401 {
		t.Fatalf("no token must stay 401, got %d", r.status)
	}

	// Upgrade by e-mail: allowed to start, still a guest until the address is verified.
	if r := do("POST", "/api/v1/auth/upgrade", "", map[string]any{"name": "Sam", "email": "sam@test.local", "password": "password123"}); r.status != 401 {
		t.Fatalf("upgrade without token = %d", r.status)
	}
	if r := do("POST", "/api/v1/auth/upgrade", token, map[string]any{"name": "Sam", "email": "x@guest.invalid", "password": "password123"}); r.status != 422 {
		t.Fatalf("placeholder domain = %d", r.status)
	}
	up := do("POST", "/api/v1/auth/upgrade", token, map[string]any{"name": "Sam", "email": "Sam@Test.local", "password": "password123"})
	if up.status != 202 || up.body["verification_required"] != true || up.body["verification_token"] == nil {
		t.Fatalf("upgrade: %d %#v", up.status, up.body)
	}
	if r := do("GET", "/api/v1/groups", token, nil); r.status != 403 {
		t.Fatalf("unverified upgrade must still be blocked, got %d", r.status)
	}
	if r := do("POST", "/api/v1/auth/login", "", map[string]any{"email": "sam@test.local", "password": "password123"}); r.status != 403 {
		t.Fatalf("login before verification = %d", r.status)
	}
	if r := do("POST", "/api/v1/auth/verify-email", "", map[string]any{"token": up.body["verification_token"]}); r.status != 200 {
		t.Fatalf("verify = %d", r.status)
	}
	if r := do("GET", "/api/v1/groups", token, nil); r.status != 200 {
		t.Fatalf("after verification the old token should reach groups, got %d", r.status)
	}
	if r := do("POST", "/api/v1/auth/login", "", map[string]any{"email": "sam@test.local", "password": "password123"}); r.status != 200 {
		t.Fatalf("login after verification = %d", r.status)
	}
	if r := do("POST", "/api/v1/auth/upgrade", token, map[string]any{"name": "Sam", "email": "other@test.local", "password": "password123"}); r.status != 409 {
		t.Fatalf("second upgrade = %d", r.status)
	}

	// E-mail already taken: the guest stays a guest.
	g2 := do("POST", "/api/v1/auth/guest", "", nil)
	t2 := g2.body["access_token"].(string)
	if r := do("POST", "/api/v1/auth/upgrade", t2, map[string]any{"name": "Dup", "email": "sam@test.local", "password": "password123"}); r.status != 409 {
		t.Fatalf("duplicate e-mail = %d", r.status)
	}
	if r := do("GET", "/api/v1/groups", t2, nil); r.status != 403 {
		t.Fatalf("still a guest, got %d", r.status)
	}
}
