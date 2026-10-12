package groups

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/settlr-org/settlr-api/internal/testutil"
)

// Only the invited account can decline; declining removes the invite from the inbox.
func TestIntegration_DeclineInviteByID(t *testing.T) {
	pool := testutil.CleanDB(t)
	srv, tokenFor := newGroupTestServer(t, pool)
	defer srv.Close()

	aliceID, aliceTok := registerViaAPI(t, srv, "Alice", "alice-decline@test.local")
	bobID, _ := registerViaAPI(t, srv, "Bob", "bob-decline@test.local")
	carolID, _ := registerViaAPI(t, srv, "Carol", "carol-decline@test.local")
	if _, err := pool.Exec(t.Context(), `INSERT INTO friendships (user_id, friend_id, status, action_by) VALUES (LEAST($1::uuid,$2::uuid), GREATEST($1::uuid,$2::uuid), 'ACCEPTED', $1)`, aliceID, bobID); err != nil {
		t.Fatal(err)
	}
	call := func(method, path, tok string, body any) (int, map[string]any) {
		b, _ := json.Marshal(body)
		req, _ := http.NewRequest(method, srv.URL+path, bytes.NewReader(b))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+tok)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var out map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&out)
		return resp.StatusCode, out
	}

	code, g := call("POST", "/api/v1/groups", aliceTok, map[string]string{"name": "Declined"})
	if code != 201 {
		t.Fatalf("create group %d", code)
	}
	gid := g["id"].(string)
	if code, out := call("POST", "/api/v1/groups/"+gid+"/invites", aliceTok, map[string]string{"user_id": bobID.String()}); code != 201 {
		t.Fatalf("create invite %d %v", code, out)
	}
	_, inbox := call("GET", "/api/v1/invites", tokenFor(bobID), nil)
	id := inbox["data"].([]any)[0].(map[string]any)["id"].(string)

	if code, _ := call("POST", "/api/v1/invites/id/not-a-uuid/decline", tokenFor(bobID), nil); code != 422 {
		t.Fatalf("bad id should be 422, got %d", code)
	}
	if code, _ := call("POST", "/api/v1/invites/id/"+id+"/decline", tokenFor(carolID), nil); code != 404 {
		t.Fatalf("another user must not decline, got %d", code)
	}
	if code, _ := call("POST", "/api/v1/invites/id/"+id+"/decline", tokenFor(bobID), nil); code != 204 {
		t.Fatalf("decline %d", code)
	}
	if _, inbox := call("GET", "/api/v1/invites", tokenFor(bobID), nil); len(inbox["data"].([]any)) != 0 {
		t.Fatalf("declined invite should leave the inbox, got %v", inbox)
	}
	if code, _ := call("POST", "/api/v1/invites/id/"+id+"/accept", tokenFor(bobID), nil); code != 404 {
		t.Fatalf("declined invite can't be accepted, got %d", code)
	}
	if code, _ := call("POST", "/api/v1/invites/id/"+id+"/decline", tokenFor(bobID), nil); code != 404 {
		t.Fatalf("second decline should be 404, got %d", code)
	}
	// Alice can invite Bob again after a decline.
	if code, out := call("POST", "/api/v1/groups/"+gid+"/invites", aliceTok, map[string]string{"user_id": bobID.String()}); code != 201 {
		t.Fatalf("re-invite %d %v", code, out)
	}
}
