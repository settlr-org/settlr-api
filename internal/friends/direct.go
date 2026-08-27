package friends

import (
	"context"
	"net/http"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nabinkhanal00/settlr-api/internal/httpx"
)

// directKey returns a deterministic unique key for a pair of users.
func directKey(a, b uuid.UUID) string {
	x, y := orderedPair(a, b)
	return "direct:" + x.String() + ":" + y.String()
}

// ensureDirectGroup returns the 1:1 ledger group for a friendship pair,
// creating it (plus memberships) if it doesn't exist yet.
func ensureDirectGroup(ctx context.Context, pool *pgxpool.Pool, a, b uuid.UUID) (uuid.UUID, error) {
	key := directKey(a, b)
	var gid uuid.UUID
	err := pool.QueryRow(ctx, `SELECT id FROM groups WHERE direct_key=$1`, key).Scan(&gid)
	if err == nil {
		return gid, nil
	}
	gid = uuid.New()
	_, err = pool.Exec(ctx,
		`INSERT INTO groups (id, name, currency, group_type, direct_key, created_by)
		 VALUES ($1, $2, 'NPR', 'DIRECT', $3, $4)`,
		gid, "Direct ledger", key, a)
	if err != nil {
		return uuid.Nil, err
	}
	for _, uid := range []uuid.UUID{a, b} {
		if _, err := pool.Exec(ctx,
			`INSERT INTO group_members (group_id, user_id, role) VALUES ($1,$2,'OWNER') ON CONFLICT DO NOTHING`,
			gid, uid); err != nil {
			return uuid.Nil, err
		}
	}
	return gid, nil
}

// GetLedger returns (creating on demand) the DIRECT group backing a friendship.
func (h *Handler) GetLedger(w http.ResponseWriter, r *http.Request) {
	uid := currentUserID(r)
	otherID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		httpx.WriteJSON(w, 422, map[string]any{"error": map[string]string{"code": "VALIDATION_ERROR", "message": "invalid user id"}})
		return
	}
	a, b := orderedPair(uid, otherID)
	// Must be an accepted friendship
	var status string
	err = h.Pool.QueryRow(r.Context(), `SELECT status FROM friendships WHERE user_id=$1 AND friend_id=$2`, a, b).Scan(&status)
	if err != nil {
		httpx.WriteError(w, r, httpx.ErrNotFound)
		return
	}
	if status != "ACCEPTED" {
		httpx.WriteJSON(w, 403, map[string]any{"error": map[string]string{"code": "FORBIDDEN", "message": "friendship not active"}})
		return
	}
	gid, err := ensureDirectGroup(r.Context(), h.Pool, a, b)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	var name string
	_ = h.Pool.QueryRow(r.Context(), `SELECT name FROM users WHERE id=$1`, otherID).Scan(&name)
	httpx.WriteJSON(w, 200, map[string]any{"group_id": gid, "friend_id": otherID, "friend_name": name, "title": "You & " + name})
}
