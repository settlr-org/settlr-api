package auth

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

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
	if err := q.CreateSession(ctx, db.CreateSessionParams{
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
