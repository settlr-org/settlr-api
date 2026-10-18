package wallet_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	db "github.com/settlr-org/settlr-api/internal/db"
	"github.com/settlr-org/settlr-api/internal/testutil"
)

// walletTables are the tables holding a user's wallet data, keyed by their owner column.
var walletTables = map[string]string{
	"accounts": "user_id", "transactions": "user_id", "tags": "user_id", "planned_payments": "user_id",
	"budgets": "user_id", "loans": "user_id", "loan_records": "user_id", "exchange_rate_overrides": "user_id",
	"user_settings": "user_id", "categories": "owner_id",
}

// personalTables hold the rest of an account's personal records.
var personalTables = map[string]string{
	"notifications": "user_id", "notification_preferences": "user_id", "oauth_identities": "user_id",
	"sessions": "user_id", "email_verification_tokens": "user_id", "password_reset_tokens": "user_id",
	"sync_keys": "user_id", "sync_records": "user_id", "sync_counters": "user_id",
}

func rows(t *testing.T, pool *pgxpool.Pool, table, col string, id uuid.UUID) int {
	t.Helper()
	extra := ""
	if table == "categories" {
		extra = " AND is_system = false"
	}
	var n int
	if err := pool.QueryRow(context.Background(), "SELECT count(*) FROM "+table+" WHERE "+col+" = $1"+extra, id).Scan(&n); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return n
}

func assertEmpty(t *testing.T, pool *pgxpool.Pool, tables map[string]string, id uuid.UUID, who string) {
	t.Helper()
	for table, col := range tables {
		if n := rows(t, pool, table, col, id); n != 0 {
			t.Errorf("%s: %s still has %d rows", who, table, n)
		}
	}
}

// seedWallet gives u accounts in two currencies, transactions, a tag, an own category, a budget, a loan, a plan,
// a rate override and a group expense paid from the account.
func seedWallet(e *env, u, other user) (groupID, expenseID string) {
	e.t.Helper()
	bank := str(e.createAccount(u, "Bank", "NPR", 100000)["id"])
	e.createAccount(u, "Savings USD", "USD", 5000)
	cat := str(e.must(201, "POST", "/api/v1/categories", u, map[string]any{"name": "Pets " + u.id.String()[:6]})["id"])
	tag := str(e.must(201, "POST", "/api/v1/tags", u, map[string]any{"name": "bills"})["id"])
	e.must(201, "POST", "/api/v1/transactions", u, map[string]any{
		"type": "EXPENSE", "account_id": bank, "amount": 2500, "title": "Lunch", "category_id": cat, "tag_ids": []string{tag}})
	e.must(201, "POST", "/api/v1/budgets", u, map[string]any{"name": "Food", "amount": 50000, "category_ids": []string{cat}})
	e.must(201, "POST", "/api/v1/loans", u, map[string]any{"name": "Friend", "type": "LEND", "amount": 1000})
	e.must(201, "POST", "/api/v1/planned", u, map[string]any{
		"type": "EXPENSE", "account_id": bank, "amount": 1000, "title": "Rent", "start_at": "2031-01-31", "interval_n": 1, "interval_unit": "MONTH"})
	e.must(200, "PUT", "/api/v1/me/rates/USD/NPR", u, map[string]any{"rate": 133})

	g := e.must(201, "POST", "/api/v1/groups", u, map[string]any{"name": "Flat", "currency": "NPR"})
	groupID = str(g["id"])
	e.must(201, "POST", "/api/v1/groups/"+groupID+"/members", u, map[string]any{"user_id": other.id.String()})
	split := []map[string]string{{"user_id": u.id.String()}, {"user_id": other.id.String()}}
	ex := e.must(201, "POST", "/api/v1/groups/"+groupID+"/expenses", u, map[string]any{
		"description": "Groceries", "amount": 10000, "paid_by": u.id.String(), "split_mode": "EQUAL", "splits": split,
		"category_id": cat, "expense_date": "2026-05-11", "payer_account_id": bank})
	expenseID = str(ex["id"])
	// The other member adds one too, so u gets a notification.
	e.must(201, "POST", "/api/v1/groups/"+groupID+"/expenses", other, map[string]any{
		"description": "Dinner", "amount": 6000, "paid_by": other.id.String(), "split_mode": "EQUAL", "splits": split,
		"expense_date": "2026-05-12"})
	return groupID, expenseID
}

func TestIntegration_DeleteWalletData(t *testing.T) {
	pool := testutil.CleanDB(t)
	e := newEnv(t, pool)
	alice := e.register("Alice", "alice-del-wallet@test.local")
	bob := e.register("Bob", "bob-del-wallet@test.local")
	e.makeFriends(alice.id, bob.id)
	gid, exID := seedWallet(e, alice, bob)
	bobBank := str(e.createAccount(bob, "Bob Bank", "NPR", 7000)["id"])
	e.must(201, "POST", "/api/v1/transactions", bob, map[string]any{"type": "EXPENSE", "account_id": bobBank, "amount": 100, "title": "Tea"})
	bobTx := rows(t, pool, "transactions", "user_id", bob.id)
	if rows(t, pool, "accounts", "user_id", alice.id) != 2 || rows(t, pool, "transactions", "user_id", alice.id) == 0 {
		t.Fatal("seed did not create wallet data")
	}

	// Without the exact confirmation nothing is erased.
	e.status(422, "DELETE", "/api/v1/me/wallet-data", alice, nil)
	e.status(422, "DELETE", "/api/v1/me/wallet-data?confirm=delete", alice, nil)
	e.status(422, "DELETE", "/api/v1/me/wallet-data?confirm=yes", alice, nil)
	if rows(t, pool, "accounts", "user_id", alice.id) != 2 {
		t.Fatal("wallet data was erased without confirmation")
	}
	// Authentication is required.
	e.status(401, "DELETE", "/api/v1/me/wallet-data?confirm=DELETE", user{}, nil)

	res := e.must(200, "DELETE", "/api/v1/me/wallet-data?confirm=DELETE", alice, nil)
	del := asMap(res["deleted"])
	if del["accounts"] != float64(2) || del["tags"] != float64(1) || del["budgets"] != float64(1) || del["loans"] != float64(1) ||
		del["planned_payments"] != float64(1) || del["rate_overrides"] != float64(1) || del["categories"] != float64(1) {
		t.Fatalf("unexpected counts: %v", del)
	}
	if n, _ := del["transactions"].(float64); n < 2 {
		t.Fatalf("expected the transactions to be counted, got %v", del["transactions"])
	}
	assertEmpty(t, pool, walletTables, alice.id, "alice after wallet wipe")

	// The account itself, groups and group history are untouched; links to the deleted account are cleared.
	e.status(200, "GET", "/api/v1/me", alice, nil)
	if len(list(e.must(200, "GET", "/api/v1/accounts", alice, nil), "data")) != 0 {
		t.Fatal("accounts still listed")
	}
	ov := e.must(200, "GET", "/api/v1/overview", alice, nil)
	if ov["balance"] != float64(0) {
		t.Fatalf("balance after wipe: %v", ov["balance"])
	}
	var payer, cat *string
	if err := pool.QueryRow(context.Background(), `SELECT payer_account_id::text, category_id::text FROM expenses WHERE id = $1`, exID).Scan(&payer, &cat); err != nil {
		t.Fatalf("group expense gone: %v", err)
	}
	if payer != nil || cat != nil {
		t.Fatalf("group expense still links to deleted data: payer=%v category=%v", payer, cat)
	}
	if n := len(list(e.must(200, "GET", "/api/v1/groups/"+gid+"/expenses", alice, nil), "data")); n != 2 {
		t.Fatalf("group expenses: want 2, got %d", n)
	}

	// Another user's wallet is untouched, and deleting twice is harmless.
	if rows(t, pool, "accounts", "user_id", bob.id) != 1 || rows(t, pool, "transactions", "user_id", bob.id) != bobTx {
		t.Fatal("bob's wallet changed")
	}
	again := asMap(e.must(200, "DELETE", "/api/v1/me/wallet-data?confirm=DELETE", alice, nil)["deleted"])
	if again["accounts"] != float64(0) || again["transactions"] != float64(0) {
		t.Fatalf("second delete should find nothing: %v", again)
	}
	// Alice can start over.
	e.createAccount(alice, "Fresh", "NPR", 1000)
}

func TestIntegration_DeleteAccount(t *testing.T) {
	pool := testutil.CleanDB(t)
	e := newEnv(t, pool)
	alice := e.register("Alice", "alice-del-acct@test.local")
	bob := e.register("Bob", "bob-del-acct@test.local")
	e.makeFriends(alice.id, bob.id)
	gid, exID := seedWallet(e, alice, bob)
	bobBank := str(e.createAccount(bob, "Bob Bank", "NPR", 7000)["id"])
	_ = bobBank

	// Personal records beyond the wallet: payment details, a Google link, a signed-in session with a refresh token.
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `UPDATE users SET bank_name='Some Bank', payment_handle='@alice', bank_qr_url='https://example.test/qr.png', avatar_url='https://example.test/a.png' WHERE id=$1`, alice.id); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO oauth_identities (provider, subject, user_id) VALUES ('google', 'google-sub-alice', $1)`, alice.id); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE users SET email_verified_at = now() WHERE id=$1`, alice.id); err != nil {
		t.Fatal(err)
	}
	// The end-to-end-encrypted copy of the wallet goes with the account.
	if _, err := pool.Exec(ctx, `INSERT INTO sync_keys (user_id, kdf, kdf_params, salt, wrapped_key) VALUES ($1, 'argon2id', '{}', '\x00112233445566778899aabbccddeeff', '\x00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff')`, alice.id); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO sync_counters (user_id, next_seq) VALUES ($1, 2)`, alice.id); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO sync_records (user_id, id, seq, nonce, ciphertext) VALUES ($1, gen_random_uuid(), 1, '\x000000000000000000000000', '\x00112233445566778899aabbccddeeff')`, alice.id); err != nil {
		t.Fatal(err)
	}
	login := e.must(200, "POST", "/api/v1/auth/login", user{}, map[string]any{"email": "alice-del-acct@test.local", "password": "password123"})
	refresh := str(login["refresh_token"])
	if rows(t, pool, "notifications", "user_id", alice.id) == 0 || rows(t, pool, "sessions", "user_id", alice.id) == 0 || rows(t, pool, "oauth_identities", "user_id", alice.id) == 0 {
		t.Fatal("seed missing notifications, sessions or the Google link")
	}
	bobAccounts := rows(t, pool, "accounts", "user_id", bob.id)

	e.must(200, "DELETE", "/api/v1/me", alice, nil)

	// Everything personal is gone ...
	assertEmpty(t, pool, walletTables, alice.id, "alice after account delete")
	assertEmpty(t, pool, personalTables, alice.id, "alice after account delete")
	var name, email, hash string
	var anon bool
	var bank, handle, qr, avatar, verified *string
	if err := pool.QueryRow(ctx, `SELECT name, email, password_hash, is_anonymous, bank_name, payment_handle, bank_qr_url, avatar_url, email_verified_at::text FROM users WHERE id=$1`, alice.id).
		Scan(&name, &email, &hash, &anon, &bank, &handle, &qr, &avatar, &verified); err != nil {
		t.Fatal(err)
	}
	if name != "Deleted User" || !anon || hash != "deleted" || email != "deleted_"+alice.id.String()+"@deleted.local" {
		t.Fatalf("user row not anonymised: %q %q %v", name, email, anon)
	}
	if bank != nil || handle != nil || qr != nil || avatar != nil || verified != nil {
		t.Fatalf("personal details survived: bank=%v handle=%v qr=%v avatar=%v verified=%v", bank, handle, qr, avatar, verified)
	}

	// ... and she can no longer get in: old access token, refresh token, password.
	e.status(401, "GET", "/api/v1/me", alice, nil)
	e.status(401, "GET", "/api/v1/accounts", alice, nil)
	e.status(401, "POST", "/api/v1/auth/refresh", user{}, map[string]any{"refresh_token": refresh})
	e.status(401, "POST", "/api/v1/auth/login", user{}, map[string]any{"email": "alice-del-acct@test.local", "password": "password123"})

	// A stale Google link can never bring the account back, even if one were left behind.
	if _, err := pool.Exec(ctx, `INSERT INTO oauth_identities (provider, subject, user_id) VALUES ('google', 'google-sub-stale', $1)`, alice.id); err != nil {
		t.Fatal(err)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if _, err := db.New(pool).WithTx(tx).GetOAuthIdentityForUpdate(ctx, "google-sub-stale"); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("Google lookup must not match an anonymised user, got %v", err)
	}

	// Shared group history stays for the other members.
	if n := len(list(e.must(200, "GET", "/api/v1/groups/"+gid+"/expenses", bob, nil), "data")); n != 2 {
		t.Fatalf("group history changed: %d expenses", n)
	}
	var paidBy uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT paid_by FROM expenses WHERE id=$1`, exID).Scan(&paidBy); err != nil || paidBy != alice.id {
		t.Fatalf("expense payer changed: %v %v", paidBy, err)
	}
	if rows(t, pool, "accounts", "user_id", bob.id) != bobAccounts {
		t.Fatal("bob's wallet changed")
	}

	// The e-mail address is free again: registering with it gives a brand-new, empty account.
	status, out := e.do("POST", "/api/v1/auth/register", "", map[string]string{"name": "Alice Again", "email": "alice-del-acct@test.local", "password": "password123"})
	if status != 201 && status != 200 {
		t.Fatalf("re-register: %d %v", status, out)
	}
	newID, _ := uuid.Parse(asMap(out["user"])["id"].(string))
	if newID == alice.id {
		t.Fatal("re-registering reused the deleted account")
	}
	if rows(t, pool, "accounts", "user_id", newID) != 0 {
		t.Fatal("new account is not empty")
	}
}
