package idempotency_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	db "github.com/settlr-org/settlr-api/internal/db"
	"github.com/settlr-org/settlr-api/internal/httpx"
	"github.com/settlr-org/settlr-api/internal/idempotency"
	"github.com/settlr-org/settlr-api/internal/testutil"
)

func TestValidKey(t *testing.T) {
	valid := []string{"a", uuid.NewString(), strings.Repeat("x", 128), "!~#$%&'()*+,-./:;<=>?@[]^_`{|}"}
	invalid := []string{"", strings.Repeat("x", 129), "has space", "tab\tkey", "naïve", "new\nline", "\x7f"}
	for _, k := range valid {
		if !idempotency.ValidKey(k) {
			t.Errorf("ValidKey(%q) = false, want true", k)
		}
	}
	for _, k := range invalid {
		if idempotency.ValidKey(k) {
			t.Errorf("ValidKey(%q) = true, want false", k)
		}
	}
}

type env struct {
	pool   *pgxpool.Pool
	srv    *httptest.Server
	userID uuid.UUID
	authAs atomic.Value // uuid.UUID the stub auth layer authenticates requests as
	calls  atomic.Int32
}

// newEnv serves `handler` behind a stub auth layer (user id on the context, as
// auth.Middleware does) and the idempotency guard, with httpx.Recover outermost
// as in cmd/settlr.
func newEnv(t *testing.T, handler func(e *env, w http.ResponseWriter, r *http.Request)) *env {
	t.Helper()
	pool := testutil.CleanDB(t)
	e := &env{pool: pool, userID: uuid.New()}
	if _, err := pool.Exec(context.Background(),
		`INSERT INTO users (id, name, email, password_hash) VALUES ($1, 'Idem', $2, 'x')`,
		e.userID, e.userID.String()+"@test.local"); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	e.authAs.Store(e.userID)
	guard := idempotency.New(db.New(pool))
	inner := guard.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		e.calls.Add(1)
		handler(e, w, r)
	}))
	authed := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		inner.ServeHTTP(w, r.WithContext(httpx.SetUserID(r.Context(), e.authAs.Load().(uuid.UUID).String())))
	})
	e.srv = httptest.NewServer(httpx.Recover(authed))
	t.Cleanup(e.srv.Close)
	return e
}

type reply struct {
	status   int
	ctype    string
	replayed string
	body     string
}

func (r reply) errorCode() string {
	var out struct {
		Error struct{ Code string } `json:"error"`
	}
	_ = json.Unmarshal([]byte(r.body), &out)
	return out.Error.Code
}

func (e *env) do(t *testing.T, method, path, key, body string) reply {
	t.Helper()
	req, _ := http.NewRequest(method, e.srv.URL+path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if key != "\x00none" {
		req.Header.Set(idempotency.HeaderKey, key)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return reply{resp.StatusCode, resp.Header.Get("Content-Type"), resp.Header.Get(idempotency.HeaderReplayed), string(b)}
}

const noKey = "\x00none"

func (e *env) rowCount(t *testing.T) int {
	t.Helper()
	var n int
	if err := e.pool.QueryRow(context.Background(), `SELECT count(*) FROM idempotency_keys WHERE user_id=$1`, e.userID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// created echoes a per-call counter so a replay is distinguishable from a re-run.
func created(e *env, w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	httpx.WriteJSON(w, http.StatusCreated, map[string]any{"call": e.calls.Load(), "echo": string(b)})
}

func TestReplayReturnsIdenticalResponse(t *testing.T) {
	e := newEnv(t, created)
	key := uuid.NewString()
	first := e.do(t, "POST", "/things", key, `{"a":1}`)
	if first.status != 201 || first.replayed != "" {
		t.Fatalf("first: %+v", first)
	}
	second := e.do(t, "POST", "/things", key, `{"a":1}`)
	if second.status != first.status || second.body != first.body || second.ctype != first.ctype {
		t.Fatalf("replay differs:\nfirst  %+v\nsecond %+v", first, second)
	}
	if second.replayed != "true" {
		t.Fatalf("missing %s header: %+v", idempotency.HeaderReplayed, second)
	}
	if n := e.calls.Load(); n != 1 {
		t.Fatalf("handler ran %d times, want 1", n)
	}
}

func TestClientErrorsAreStoredAndReplayed(t *testing.T) {
	e := newEnv(t, func(e *env, w http.ResponseWriter, r *http.Request) {
		httpx.WriteError(w, r, httpx.ErrValidation)
	})
	key := uuid.NewString()
	first := e.do(t, "PATCH", "/things/1", key, `{}`)
	second := e.do(t, "PATCH", "/things/1", key, `{}`)
	if first.status != 422 || second.status != 422 || second.body != first.body || second.replayed != "true" {
		t.Fatalf("4xx not replayed: %+v / %+v", first, second)
	}
	if e.calls.Load() != 1 {
		t.Fatalf("handler ran %d times, want 1", e.calls.Load())
	}
}

func TestEmptyResponseReplayed(t *testing.T) {
	e := newEnv(t, func(e *env, w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })
	key := uuid.NewString()
	first := e.do(t, "DELETE", "/things/1", key, "")
	second := e.do(t, "DELETE", "/things/1", key, "")
	if first.status != 204 || second.status != 204 || second.replayed != "true" || second.body != "" {
		t.Fatalf("204 replay: %+v / %+v", first, second)
	}
	if e.calls.Load() != 1 {
		t.Fatalf("handler ran %d times, want 1", e.calls.Load())
	}
}

func TestConcurrentDuplicateGets409(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	e := newEnv(t, func(e *env, w http.ResponseWriter, r *http.Request) {
		close(entered)
		<-release
		created(e, w, r)
	})
	key := uuid.NewString()
	done := make(chan reply, 1)
	go func() { done <- e.do(t, "POST", "/things", key, `{}`) }()
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("first request never reached the handler")
	}
	dup := e.do(t, "POST", "/things", key, `{}`)
	close(release)
	first := <-done
	if first.status != 201 {
		t.Fatalf("first: %+v", first)
	}
	if dup.status != 409 || dup.errorCode() != "IDEMPOTENCY_IN_PROGRESS" {
		t.Fatalf("duplicate: %+v", dup)
	}
	if e.calls.Load() != 1 {
		t.Fatalf("handler ran %d times, want 1", e.calls.Load())
	}
	if again := e.do(t, "POST", "/things", key, `{}`); again.replayed != "true" || again.body != first.body {
		t.Fatalf("after completion: %+v", again)
	}
}

func TestKeyReuseForDifferentRequestGets422(t *testing.T) {
	e := newEnv(t, created)
	key := uuid.NewString()
	if r := e.do(t, "POST", "/things", key, `{"a":1}`); r.status != 201 {
		t.Fatalf("first: %+v", r)
	}
	for _, c := range []struct{ method, path, body string }{
		{"POST", "/other", `{"a":1}`},
		{"PUT", "/things", `{"a":1}`},
		{"POST", "/things", `{"a":2}`},
	} {
		r := e.do(t, c.method, c.path, key, c.body)
		if r.status != 422 || r.errorCode() != "IDEMPOTENCY_KEY_REUSED" {
			t.Errorf("%s %s %s: %+v", c.method, c.path, c.body, r)
		}
	}
	if e.calls.Load() != 1 {
		t.Fatalf("handler ran %d times, want 1", e.calls.Load())
	}
}

func TestServerErrorReleasesKey(t *testing.T) {
	e := newEnv(t, func(e *env, w http.ResponseWriter, r *http.Request) {
		if e.calls.Load() == 1 {
			httpx.WriteError(w, r, fmt.Errorf("boom"))
			return
		}
		created(e, w, r)
	})
	key := uuid.NewString()
	if r := e.do(t, "POST", "/things", key, `{}`); r.status != 500 {
		t.Fatalf("first: %+v", r)
	}
	if n := e.rowCount(t); n != 0 {
		t.Fatalf("reservation kept after 5xx: %d rows", n)
	}
	r := e.do(t, "POST", "/things", key, `{}`)
	if r.status != 201 || r.replayed != "" || e.calls.Load() != 2 {
		t.Fatalf("retry after 5xx should re-run: %+v calls=%d", r, e.calls.Load())
	}
}

func TestPanicReleasesKey(t *testing.T) {
	e := newEnv(t, func(e *env, w http.ResponseWriter, r *http.Request) {
		if e.calls.Load() == 1 {
			panic("handler exploded")
		}
		created(e, w, r)
	})
	key := uuid.NewString()
	if r := e.do(t, "POST", "/things", key, `{}`); r.status != 500 {
		t.Fatalf("first: %+v", r)
	}
	r := e.do(t, "POST", "/things", key, `{}`)
	if r.status != 201 || e.calls.Load() != 2 {
		t.Fatalf("retry after panic should re-run: %+v calls=%d", r, e.calls.Load())
	}
}

func TestMissingHeaderUnchanged(t *testing.T) {
	e := newEnv(t, created)
	a := e.do(t, "POST", "/things", noKey, `{}`)
	b := e.do(t, "POST", "/things", noKey, `{}`)
	if a.status != 201 || b.status != 201 || a.replayed != "" || b.replayed != "" || a.body == b.body {
		t.Fatalf("no-key requests should both run: %+v / %+v", a, b)
	}
	if e.rowCount(t) != 0 {
		t.Fatal("no-key requests must not store anything")
	}
}

func TestReadsIgnoreHeader(t *testing.T) {
	e := newEnv(t, created)
	key := "not a valid key"
	a := e.do(t, "GET", "/things", key, "")
	b := e.do(t, "GET", "/things", key, "")
	if a.status != 201 || b.status != 201 || b.replayed != "" || e.calls.Load() != 2 || e.rowCount(t) != 0 {
		t.Fatalf("GET should bypass idempotency: %+v / %+v", a, b)
	}
}

func TestInvalidKeyRejected(t *testing.T) {
	e := newEnv(t, created)
	for _, key := range []string{"", strings.Repeat("k", 129), "with space", "naïve"} {
		r := e.do(t, "POST", "/things", key, `{}`)
		if r.status != 400 || r.errorCode() != "IDEMPOTENCY_KEY_INVALID" {
			t.Errorf("key %q: %+v", key, r)
		}
	}
	if e.calls.Load() != 0 {
		t.Fatalf("handler ran %d times for invalid keys", e.calls.Load())
	}
}

func TestExpiredKeyIsOverwrittenAndCleanedUp(t *testing.T) {
	e := newEnv(t, created)
	ctx := context.Background()
	key := uuid.NewString()
	first := e.do(t, "POST", "/things", key, `{}`)
	if _, err := e.pool.Exec(ctx, `UPDATE idempotency_keys SET expires_at = now() - interval '1 second' WHERE user_id=$1`, e.userID); err != nil {
		t.Fatal(err)
	}
	// Even a different request may reuse an expired key.
	second := e.do(t, "POST", "/elsewhere", key, `{"b":2}`)
	if second.status != 201 || second.replayed != "" || second.body == first.body || e.calls.Load() != 2 {
		t.Fatalf("expired key should run again: %+v calls=%d", second, e.calls.Load())
	}
	if e.rowCount(t) != 1 {
		t.Fatalf("rows = %d, want 1 (overwritten in place)", e.rowCount(t))
	}

	other := uuid.NewString()
	e.do(t, "POST", "/things", other, `{}`)
	if _, err := e.pool.Exec(ctx, `UPDATE idempotency_keys SET expires_at = now() - interval '1 second' WHERE user_id=$1 AND key=$2`, e.userID, other); err != nil {
		t.Fatal(err)
	}
	idempotency.Cleanup(ctx, db.New(e.pool))
	var keys []string
	rows, _ := e.pool.Query(ctx, `SELECT key FROM idempotency_keys WHERE user_id=$1`, e.userID)
	for rows.Next() {
		var k string
		_ = rows.Scan(&k)
		keys = append(keys, k)
	}
	rows.Close()
	if len(keys) != 1 || keys[0] != key {
		t.Fatalf("after cleanup keys = %v, want only the live %q", keys, key)
	}
}

func TestStalePendingReservationIsTakenOver(t *testing.T) {
	e := newEnv(t, created)
	key := uuid.NewString()
	// Simulate a process that died after reserving: pending, older than the lease.
	if _, err := e.pool.Exec(context.Background(), `
		INSERT INTO idempotency_keys (user_id, key, request_method, request_path, request_hash, lock_token, created_at, expires_at)
		VALUES ($1, $2, 'POST', '/things', 'x', gen_random_uuid(), now() - interval '10 minutes', now() + interval '1 hour')`,
		e.userID, key); err != nil {
		t.Fatal(err)
	}
	r := e.do(t, "POST", "/things", key, `{}`)
	if r.status != 201 || e.calls.Load() != 1 {
		t.Fatalf("stale pending reservation should be taken over: %+v", r)
	}
}

func TestKeysAreScopedPerUser(t *testing.T) {
	e := newEnv(t, created)
	key := uuid.NewString()
	e.do(t, "POST", "/things", key, `{}`)
	// Same key for another user is an independent reservation.
	other := uuid.New()
	if _, err := e.pool.Exec(context.Background(),
		`INSERT INTO users (id, name, email, password_hash) VALUES ($1, 'Other', $2, 'x')`, other, other.String()+"@test.local"); err != nil {
		t.Fatal(err)
	}
	e.authAs.Store(other)
	r := e.do(t, "POST", "/things", key, `{}`)
	if r.status != 201 || r.replayed != "" || e.calls.Load() != 2 {
		t.Fatalf("other user's request should run: %+v", r)
	}
}
