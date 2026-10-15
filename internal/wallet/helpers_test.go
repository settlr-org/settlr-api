package wallet_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/settlr-org/settlr-api/internal/auth"
	"github.com/settlr-org/settlr-api/internal/categories"
	"github.com/settlr-org/settlr-api/internal/config"
	db "github.com/settlr-org/settlr-api/internal/db"
	"github.com/settlr-org/settlr-api/internal/expenses"
	"github.com/settlr-org/settlr-api/internal/groups"
	"github.com/settlr-org/settlr-api/internal/personal"
	"github.com/settlr-org/settlr-api/internal/rates"
	"github.com/settlr-org/settlr-api/internal/search"
	"github.com/settlr-org/settlr-api/internal/settlements"
	"github.com/settlr-org/settlr-api/internal/wallet"
)

// fakeRates serves fixed rates so tests never hit the network.
type fakeRates map[string]map[string]float64

func (f fakeRates) Latest(_ context.Context, base string) (map[string]float64, error) {
	if r, ok := f[base]; ok {
		return r, nil
	}
	return nil, fmt.Errorf("no rates for %s", base)
}

var testRates = fakeRates{
	"USD": {"NPR": 133, "EUR": 0.9},
	"EUR": {"USD": 1 / 0.9},
}

type env struct {
	t    *testing.T
	srv  *httptest.Server
	pool *pgxpool.Pool
}

func newEnv(t *testing.T, pool *pgxpool.Pool) *env {
	t.Helper()
	cfg := config.Config{
		JWTSecret:         "test-jwt-secret-32-chars-min!!!!",
		JWTRefreshSecret:  "test-refresh-secret-32-chars!!",
		JWTExpiryMinutes:  15,
		RefreshExpiryDays: 30,
		Env:               "test",
	}
	authSvc := &auth.Service{Pool: pool, Cfg: cfg}
	mux := http.NewServeMux()
	(&auth.Handler{Svc: authSvc}).RegisterRoutes(mux)
	authMw := auth.Middleware(authSvc)
	(&groups.Handler{Pool: pool}).RegisterRoutes(mux, authMw)
	(&expenses.Handler{Pool: pool}).RegisterRoutes(mux, authMw)
	(&settlements.Handler{Pool: pool}).RegisterRoutes(mux, authMw)
	(&categories.Handler{Pool: pool}).RegisterRoutes(mux, authMw)
	// Queries is set up front (as in main) so concurrent requests don't race on lazy init.
	queries := db.New(pool)
	(&personal.Handler{Pool: pool, Queries: queries}).RegisterRoutes(mux, authMw)
	(&search.Handler{Pool: pool, Queries: queries}).RegisterRoutes(mux, authMw)
	(&wallet.Handler{Pool: pool, Queries: queries, Rates: rates.NewConverter(testRates)}).RegisterRoutes(mux, authMw)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &env{t: t, srv: srv, pool: pool}
}

type user struct {
	id    uuid.UUID
	token string
}

func (e *env) register(name, email string) user {
	e.t.Helper()
	status, out := e.do("POST", "/api/v1/auth/register", "", map[string]string{"name": name, "email": email, "password": "password123"})
	if status != 201 && status != 200 {
		e.t.Fatalf("register %s: %d %v", email, status, out)
	}
	u := out["user"].(map[string]any)
	id, _ := uuid.Parse(u["id"].(string))
	return user{id: id, token: out["access_token"].(string)}
}

// do sends a JSON request and decodes a JSON object response.
func (e *env) do(method, path, token string, body any) (int, map[string]any) {
	e.t.Helper()
	status, raw := e.raw(method, path, token, body, nil)
	out := map[string]any{}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &out); err != nil {
			e.t.Fatalf("%s %s: decode %q: %v", method, path, raw, err)
		}
	}
	return status, out
}

func (e *env) raw(method, path, token string, body any, headers map[string]string) (int, []byte) {
	e.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, e.srv.URL+path, rd)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

// must asserts the status and returns the body.
func (e *env) must(want int, method, path string, u user, body any) map[string]any {
	e.t.Helper()
	status, out := e.do(method, path, u.token, body)
	if status != want {
		e.t.Fatalf("%s %s: status %d want %d: %v", method, path, status, want, out)
	}
	return out
}

func (e *env) status(want int, method, path string, u user, body any) {
	e.t.Helper()
	e.must(want, method, path, u, body)
}

func num(v any) int64 {
	switch x := v.(type) {
	case float64:
		return int64(x)
	case nil:
		return 0
	}
	panic(fmt.Sprintf("not a number: %#v", v))
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

func list(m map[string]any, key string) []map[string]any {
	arr, _ := m[key].([]any)
	out := make([]map[string]any, 0, len(arr))
	for _, x := range arr {
		out = append(out, x.(map[string]any))
	}
	return out
}

func (e *env) accountsByName(u user) map[string]map[string]any {
	e.t.Helper()
	out := map[string]map[string]any{}
	for _, a := range list(e.must(200, "GET", "/api/v1/accounts", u, nil), "data") {
		out[str(a["name"])] = a
	}
	return out
}

func (e *env) createAccount(u user, name, currency string, initial int64) map[string]any {
	e.t.Helper()
	return e.must(201, "POST", "/api/v1/accounts", u, map[string]any{"name": name, "currency": currency, "initial_balance": initial})
}

func (e *env) makeFriends(a, b uuid.UUID) {
	e.t.Helper()
	if _, err := e.pool.Exec(context.Background(),
		`INSERT INTO friendships (id, user_id, friend_id, status, action_by) VALUES (gen_random_uuid(), LEAST($1::uuid,$2::uuid), GREATEST($1::uuid,$2::uuid), 'ACCEPTED', $1::uuid)`,
		a, b); err != nil {
		e.t.Fatalf("make friends: %v", err)
	}
}

func (e *env) systemCategory(name string) string {
	e.t.Helper()
	var id string
	if err := e.pool.QueryRow(context.Background(),
		`INSERT INTO categories (name, is_system) VALUES ($1, true)
		 ON CONFLICT (lower(name)) WHERE is_system DO UPDATE SET name = EXCLUDED.name RETURNING id`, name).Scan(&id); err != nil {
		e.t.Fatalf("system category: %v", err)
	}
	return id
}
