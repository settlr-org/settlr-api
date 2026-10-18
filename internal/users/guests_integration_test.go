package users_test

import (
	"testing"

	"github.com/google/uuid"

	"github.com/settlr-org/settlr-api/internal/testutil"
	"github.com/settlr-org/settlr-api/internal/users"
)

func TestPurgeStaleGuests(t *testing.T) {
	pool := testutil.CleanDB(t)
	ctx := t.Context()
	mk := func(guest bool, ageDays int, liveSession bool) uuid.UUID {
		id := uuid.New()
		if _, err := pool.Exec(ctx, `INSERT INTO users (id, name, email, is_guest, created_at) VALUES ($1, 'G', $2, $3, now() - make_interval(days => $4))`,
			id, id.String()+"@guest.invalid", guest, ageDays); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `INSERT INTO accounts (user_id, name, currency) VALUES ($1, 'Cash', 'USD')`, id); err != nil {
			t.Fatal(err)
		}
		exp := "now() - interval '1 day'"
		if liveSession {
			exp = "now() + interval '10 days'"
		}
		if _, err := pool.Exec(ctx, `INSERT INTO sessions (user_id, refresh_token_hash, expires_at) VALUES ($1, $2, `+exp+`)`, id, id.String()); err != nil {
			t.Fatal(err)
		}
		return id
	}
	stale := mk(true, 90, false)
	young := mk(true, 5, false)
	live := mk(true, 90, true)
	real := mk(false, 90, false)

	n, err := users.PurgeStaleGuests(ctx, pool)
	if err != nil || n != 1 {
		t.Fatalf("purged %d, err %v; want 1", n, err)
	}
	exists := func(id uuid.UUID) bool {
		var c int
		_ = pool.QueryRow(ctx, `SELECT count(*) FROM users WHERE id = $1`, id).Scan(&c)
		return c == 1
	}
	if exists(stale) || !exists(young) || !exists(live) || !exists(real) {
		t.Fatalf("wrong set purged: stale=%v young=%v live=%v real=%v", exists(stale), exists(young), exists(live), exists(real))
	}
	var left int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM accounts WHERE user_id = $1`, stale).Scan(&left)
	if left != 0 {
		t.Fatalf("stale guest accounts left: %d", left)
	}
}
