package wallet_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/settlr-org/settlr-api/internal/testutil"
)

func (e *env) notifications(userID string, typ string) []map[string]any {
	e.t.Helper()
	rows, err := e.pool.Query(context.Background(), `SELECT title, body, data FROM notifications WHERE user_id = $1 AND type = $2 ORDER BY created_at`, userID, typ)
	if err != nil {
		e.t.Fatal(err)
	}
	defer rows.Close()
	var out []map[string]any
	for rows.Next() {
		var title, body string
		var data []byte
		if err := rows.Scan(&title, &body, &data); err != nil {
			e.t.Fatal(err)
		}
		d := map[string]any{}
		_ = json.Unmarshal(data, &d)
		out = append(out, map[string]any{"title": title, "body": body, "data": d})
	}
	return out
}

// Notifications name the group, the people and the amounts instead of generic text.
func TestIntegration_NotificationsAreSpecific(t *testing.T) {
	e := newEnv(t, testutil.CleanDB(t))
	alice := e.register("Alice \"Al\" Smith", "alice-notif@test.local")
	bob := e.register("Bob", "bob-notif@test.local")
	e.makeFriends(alice.id, bob.id)
	g := e.must(201, "POST", "/api/v1/groups", alice, map[string]any{"name": "Trip to Pokhara", "currency": "USD"})
	gid := str(g["id"])

	inv := e.must(201, "POST", "/api/v1/groups/"+gid+"/invites", alice, map[string]any{"user_id": bob.id.String()})
	n := e.notifications(bob.id.String(), "GROUP_INVITATION")
	if len(n) != 1 {
		t.Fatalf("invitation notifications: %v", n)
	}
	d := n[0]["data"].(map[string]any)
	if n[0]["title"] != "Invitation to Trip to Pokhara" || n[0]["body"] != "Alice \"Al\" Smith invited you to join Trip to Pokhara" ||
		str(d["group_name"]) != "Trip to Pokhara" || str(d["inviter_name"]) != "Alice \"Al\" Smith" || str(d["group_id"]) != gid || str(d["token"]) != str(inv["token"]) {
		t.Fatalf("invitation: %v", n[0])
	}

	e.must(201, "POST", "/api/v1/groups/"+gid+"/members", alice, map[string]any{"user_id": bob.id.String()})
	e.must(201, "POST", "/api/v1/groups/"+gid+"/expenses", alice, map[string]any{
		"description": "Boat ride", "amount": 12345, "paid_by": alice.id.String(), "split_mode": "EQUAL", "expense_date": "2026-05-10",
		"splits": []map[string]string{{"user_id": alice.id.String()}, {"user_id": bob.id.String()}}})
	n = e.notifications(bob.id.String(), "EXPENSE_ADDED")
	if len(n) != 1 || n[0]["title"] != "New expense in Trip to Pokhara" || !strings.Contains(str(n[0]["body"]), `Alice "Al" Smith added "Boat ride" (123.45 USD)`) {
		t.Fatalf("expense notification: %v", n)
	}
	if d := n[0]["data"].(map[string]any); str(d["description"]) != "Boat ride" || num(d["amount"]) != 12345 || str(d["added_by_name"]) == "" || str(d["group_name"]) != "Trip to Pokhara" || d["expense_id"] == nil {
		t.Fatalf("expense data: %v", d)
	}
	if got := e.notifications(alice.id.String(), "EXPENSE_ADDED"); len(got) != 0 {
		t.Fatalf("the actor is not notified: %v", got)
	}

	e.must(201, "POST", "/api/v1/groups/"+gid+"/settlements", bob, map[string]any{"from_user": bob.id.String(), "to_user": alice.id.String(), "amount": 500})
	n = e.notifications(alice.id.String(), "SETTLEMENT_RECORDED")
	if len(n) != 1 || n[0]["title"] != "Settlement recorded in Trip to Pokhara" || n[0]["body"] != "Bob paid Alice \"Al\" Smith 5.00 USD" {
		t.Fatalf("settlement notification: %v", n)
	}
	if d := n[0]["data"].(map[string]any); str(d["payer_name"]) != "Bob" || num(d["amount"]) != 500 || str(d["currency"]) != "USD" || str(d["group_id"]) != gid {
		t.Fatalf("settlement data: %v", d)
	}
}
