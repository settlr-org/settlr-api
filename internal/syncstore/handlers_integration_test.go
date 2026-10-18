package syncstore_test

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/settlr-org/settlr-api/internal/auth"
	"github.com/settlr-org/settlr-api/internal/config"
	"github.com/settlr-org/settlr-api/internal/syncstore"
	"github.com/settlr-org/settlr-api/internal/testutil"
)

type fixture struct {
	t   *testing.T
	srv *httptest.Server
}

func b64(b []byte) string { return base64.StdEncoding.EncodeToString(b) }

func randBytes(n int) []byte {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return b
}

func newFixture(t *testing.T) (*fixture, []string) {
	pool := testutil.CleanDB(t)
	cfg := config.Config{JWTSecret: "test-jwt-secret-32-chars-min!!!!", JWTRefreshSecret: "test-refresh-secret-32-chars!!", JWTExpiryMinutes: 15, RefreshExpiryDays: 30, Env: "test"}
	svc := &auth.Service{Pool: pool, Cfg: cfg}
	mux := http.NewServeMux()
	(&auth.Handler{Svc: svc}).RegisterRoutes(mux)
	(&syncstore.Handler{Pool: pool}).RegisterRoutes(mux, auth.Middleware(svc))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	f := &fixture{t: t, srv: srv}
	var tokens []string
	for _, e := range []string{"sync-a@test.local", "sync-b@test.local"} {
		_, out := f.do("POST", "/api/v1/auth/register", "", map[string]string{"name": "U", "email": e, "password": "password123"})
		tokens = append(tokens, out["access_token"].(string))
	}
	return f, tokens
}

func (f *fixture) do(method, path, token string, body any) (int, map[string]any) {
	f.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, f.srv.URL+path, rd)
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		f.t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	out := map[string]any{}
	_ = json.Unmarshal(raw, &out)
	return resp.StatusCode, out
}

func keyBody(version int) map[string]any {
	return map[string]any{"kdf": "argon2id", "kdf_params": map[string]any{"m": 65536, "t": 3, "p": 1}, "salt": b64(randBytes(16)), "wrapped_key": b64(randBytes(60)), "expected_version": version}
}

func rec(id string, deleted bool) map[string]any {
	return map[string]any{"id": id, "deleted": deleted, "nonce": b64(randBytes(12)), "ciphertext": b64(randBytes(48))}
}

func TestKeyLifecycle(t *testing.T) {
	f, tok := newFixture(t)
	a := tok[0]
	if st, _ := f.do("GET", "/api/v1/sync/key", a, nil); st != 404 {
		t.Fatalf("no key yet: %d", st)
	}
	if st, _ := f.do("PUT", "/api/v1/sync/key", a, map[string]any{"kdf": "argon2id", "kdf_params": map[string]any{}, "salt": "x", "wrapped_key": "y"}); st != 422 {
		t.Fatalf("bad material = %d", st)
	}
	if st, out := f.do("PUT", "/api/v1/sync/key", a, keyBody(0)); st != 200 || out["version"].(float64) != 1 {
		t.Fatalf("create: %d %v", st, out)
	}
	if st, _ := f.do("PUT", "/api/v1/sync/key", a, keyBody(0)); st != 409 {
		t.Fatalf("second create must conflict, got %d", st)
	}
	if st, out := f.do("GET", "/api/v1/sync/key", a, nil); st != 200 || out["kdf"] != "argon2id" || out["version"].(float64) != 1 {
		t.Fatalf("get: %d %v", st, out)
	}
	if st, out := f.do("PUT", "/api/v1/sync/key", a, keyBody(1)); st != 200 || out["version"].(float64) != 2 {
		t.Fatalf("rotate: %d %v", st, out)
	}
	if st, _ := f.do("PUT", "/api/v1/sync/key", a, keyBody(1)); st != 409 {
		t.Fatalf("stale version must conflict, got %d", st)
	}
	// another user has none
	if st, _ := f.do("GET", "/api/v1/sync/key", tok[1], nil); st != 404 {
		t.Fatalf("keys are per user, got %d", st)
	}
	if st, _ := f.do("GET", "/api/v1/sync/key", "", nil); st != 401 {
		t.Fatalf("needs auth, got %d", st)
	}
}

func TestPushPullOrderingAndIsolation(t *testing.T) {
	f, tok := newFixture(t)
	a, b := tok[0], tok[1]
	if st, _ := f.do("POST", "/api/v1/sync/records", a, map[string]any{"records": []any{rec("11111111-1111-1111-1111-111111111111", false)}}); st != 422 {
		t.Fatalf("push without a key = %d", st)
	}
	f.do("PUT", "/api/v1/sync/key", a, keyBody(0))
	f.do("PUT", "/api/v1/sync/key", b, keyBody(0))
	ids := []string{"11111111-1111-1111-1111-111111111111", "22222222-2222-2222-2222-222222222222", "33333333-3333-3333-3333-333333333333"}
	st, out := f.do("POST", "/api/v1/sync/records", a, map[string]any{"records": []any{rec(ids[0], false), rec(ids[1], false), rec(ids[2], false)}})
	if st != 200 || out["accepted"].(float64) != 3 || out["latest_seq"].(float64) != 3 {
		t.Fatalf("push: %d %v", st, out)
	}
	// updating one record moves it to the end of the log
	if st, out := f.do("POST", "/api/v1/sync/records", a, map[string]any{"records": []any{rec(ids[0], true)}}); st != 200 || out["latest_seq"].(float64) != 4 {
		t.Fatalf("update: %d %v", st, out)
	}
	_, pull := f.do("GET", "/api/v1/sync/changes?since=0", a, nil)
	recs := pull["records"].([]any)
	if len(recs) != 3 || pull["next_seq"].(float64) != 4 || pull["has_more"].(bool) {
		t.Fatalf("pull: %v", pull)
	}
	last := recs[2].(map[string]any)
	if last["id"] != ids[0] || last["deleted"] != true || last["seq"].(float64) != 4 {
		t.Fatalf("the updated record must be last: %v", last)
	}
	_, page := f.do("GET", "/api/v1/sync/changes?since=0&limit=2", a, nil)
	if len(page["records"].([]any)) != 2 || !page["has_more"].(bool) || page["next_seq"].(float64) != 3 {
		t.Fatalf("paging: %v", page)
	}
	_, after := f.do("GET", "/api/v1/sync/changes?since=4", a, nil)
	if len(after["records"].([]any)) != 0 || after["next_seq"].(float64) != 4 {
		t.Fatalf("nothing after the cursor: %v", after)
	}
	// the other user sees none of it and has its own sequence
	_, other := f.do("GET", "/api/v1/sync/changes?since=0", b, nil)
	if len(other["records"].([]any)) != 0 {
		t.Fatalf("isolation: %v", other)
	}
	if _, out := f.do("POST", "/api/v1/sync/records", b, map[string]any{"records": []any{rec(ids[0], false)}}); out["latest_seq"].(float64) != 1 {
		t.Fatalf("per-user sequence: %v", out)
	}
	if _, st := f.do("GET", "/api/v1/sync/status", a, nil); st["records"].(float64) != 3 || st["has_key"] != true || st["latest_seq"].(float64) != 4 {
		t.Fatalf("status: %v", st)
	}
}

func TestRecordValidationAndWipe(t *testing.T) {
	f, tok := newFixture(t)
	a := tok[0]
	f.do("PUT", "/api/v1/sync/key", a, keyBody(0))
	good := "11111111-1111-1111-1111-111111111111"
	bad := []map[string]any{
		{"id": "00000000-0000-0000-0000-000000000000", "nonce": b64(randBytes(12)), "ciphertext": b64(randBytes(48))},
		{"id": good, "nonce": b64(randBytes(8)), "ciphertext": b64(randBytes(48))},
		{"id": good, "nonce": b64(randBytes(12)), "ciphertext": b64(randBytes(8))},
		{"id": good, "nonce": b64(randBytes(12)), "ciphertext": b64(randBytes(70 << 10))},
		{"id": good, "nonce": "!!", "ciphertext": "!!"},
	}
	for i, r := range bad {
		if st, _ := f.do("POST", "/api/v1/sync/records", a, map[string]any{"records": []any{r}}); st != 422 {
			t.Fatalf("bad record %d accepted (%d)", i, st)
		}
	}
	if st, _ := f.do("POST", "/api/v1/sync/records", a, map[string]any{"records": []any{}}); st != 422 {
		t.Fatalf("empty batch = %d", st)
	}
	big := make([]any, 201)
	for i := range big {
		big[i] = rec("44444444-4444-4444-4444-4444444444"+string(rune('0'+i%10))+string(rune('0'+(i/10)%10)), false)
	}
	if st, _ := f.do("POST", "/api/v1/sync/records", a, map[string]any{"records": big}); st != 422 {
		t.Fatalf("oversized batch = %d", st)
	}
	f.do("POST", "/api/v1/sync/records", a, map[string]any{"records": []any{rec(good, false)}})
	if st, _ := f.do("DELETE", "/api/v1/sync", a, nil); st != 422 {
		t.Fatalf("wipe needs confirmation, got %d", st)
	}
	if st, _ := f.do("DELETE", "/api/v1/sync?confirm=DELETE", a, nil); st != 200 {
		t.Fatalf("wipe = %d", st)
	}
	if st, _ := f.do("GET", "/api/v1/sync/key", a, nil); st != 404 {
		t.Fatalf("the key is gone, got %d", st)
	}
	_, pull := f.do("GET", "/api/v1/sync/changes?since=0", a, nil)
	if len(pull["records"].([]any)) != 0 {
		t.Fatalf("records are gone: %v", pull)
	}
}
