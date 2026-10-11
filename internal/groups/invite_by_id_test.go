package groups

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/settlr-org/settlr-api/internal/testutil"
)

// The invitee's inbox (GET /invites) has no raw token, so invites must be acceptable by id —
// but only by the invited account.
func TestIntegration_AcceptInviteByID(t *testing.T) {
	pool := testutil.CleanDB(t)
	srv, tokenFor := newGroupTestServer(t, pool)
	defer srv.Close()

	aliceID, aliceTok := registerViaAPI(t, srv, "Alice", "alice-inbox@test.local")
	bobID, _ := registerViaAPI(t, srv, "Bob", "bob-inbox@test.local")
	carolID, _ := registerViaAPI(t, srv, "Carol", "carol-inbox@test.local")
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

	code, g := call("POST", "/api/v1/groups", aliceTok, map[string]string{"name": "Inbox"})
	if code != 201 {
		t.Fatalf("create group %d", code)
	}
	gid := g["id"].(string)
	if code, out := call("POST", "/api/v1/groups/"+gid+"/invites", aliceTok, map[string]string{"user_id": bobID.String()}); code != 201 {
		t.Fatalf("create invite %d %v", code, out)
	}

	code, inbox := call("GET", "/api/v1/invites", tokenFor(bobID), nil)
	if code != 200 || len(inbox["data"].([]any)) != 1 {
		t.Fatalf("bob inbox %d %v", code, inbox)
	}
	item := inbox["data"].([]any)[0].(map[string]any)
	if item["inviter_name"] != "Alice" {
		t.Fatalf("inbox should name the inviter, got %v", item)
	}
	id := item["id"].(string)

	if code, _ := call("POST", "/api/v1/invites/id/"+id+"/accept", tokenFor(carolID), nil); code != 403 {
		t.Fatalf("another user must not accept, got %d", code)
	}
	code, out := call("POST", "/api/v1/invites/id/"+id+"/accept", tokenFor(bobID), nil)
	if code != 200 || out["group_id"] != gid {
		t.Fatalf("accept by id %d %v", code, out)
	}
	if code, _ := call("POST", "/api/v1/invites/id/"+id+"/accept", tokenFor(bobID), nil); code != 409 {
		t.Fatalf("second accept should be 409, got %d", code)
	}
	if code, _ := call("GET", "/api/v1/groups/"+gid, tokenFor(bobID), nil); code != 200 {
		t.Fatalf("bob should now be a member, got %d", code)
	}
}
