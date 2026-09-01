package friends

import (
	"context"
	"net/http"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	db "github.com/settlr-org/settlr-api/internal/db"
	"github.com/settlr-org/settlr-api/internal/httpx"
)

// directKey returns a deterministic unique key for a pair of users.
func directKey(a, b uuid.UUID) string {
	x, y := orderedPair(a, b)
	return "direct:" + x.String() + ":" + y.String()
}

// ensureDirectGroup returns the 1:1 ledger group for a friendship pair,
// creating it (plus memberships) if it doesn't exist yet.
// This is the sqlc-migrated Handler method; use Handler.ensureDirectGroup where possible.
func (h *Handler) ensureDirectGroup(ctx context.Context, a, b uuid.UUID) (uuid.UUID, error) {
	h.ensureQueries()
	key := directKey(a, b)
	gid, err := h.Queries.GetDirectGroupByKey(ctx, pgtype.Text{String: key, Valid: true})
	if err == nil {
		return gid, nil
	}
	if err != pgx.ErrNoRows {
		return uuid.Nil, err
	}
	gid = uuid.New()
	if err := h.Queries.CreateDirectGroup(ctx, db.CreateDirectGroupParams{
		ID:        gid,
		Name:      "Direct ledger",
		DirectKey: pgtype.Text{String: key, Valid: true},
		CreatedBy: a,
	}); err != nil {
		return uuid.Nil, err
	}
	for _, uid := range []uuid.UUID{a, b} {
		if err := h.Queries.AddDirectGroupMember(ctx, db.AddDirectGroupMemberParams{GroupID: gid, UserID: uid}); err != nil {
			return uuid.Nil, err
		}
	}
	return gid, nil
}

// ensureDirectGroup is the legacy pool-based helper retained for compatibility.
// It now delegates to sqlc via db.New(pool) to satisfy the migration threshold
// (no manual Pool.QueryRow/Exec remains in this file).
func ensureDirectGroup(ctx context.Context, pool *pgxpool.Pool, a, b uuid.UUID) (uuid.UUID, error) {
	q := db.New(pool)
	key := directKey(a, b)
	gid, err := q.GetDirectGroupByKey(ctx, pgtype.Text{String: key, Valid: true})
	if err == nil {
		return gid, nil
	}
	if err != pgx.ErrNoRows {
		return uuid.Nil, err
	}
	gid = uuid.New()
	if err := q.CreateDirectGroup(ctx, db.CreateDirectGroupParams{
		ID:        gid,
		Name:      "Direct ledger",
		DirectKey: pgtype.Text{String: key, Valid: true},
		CreatedBy: a,
	}); err != nil {
		return uuid.Nil, err
	}
	for _, uid := range []uuid.UUID{a, b} {
		if err := q.AddDirectGroupMember(ctx, db.AddDirectGroupMemberParams{GroupID: gid, UserID: uid}); err != nil {
			return uuid.Nil, err
		}
	}
	return gid, nil
}

// GetLedger returns (creating on demand) the DIRECT group backing a friendship.
func (h *Handler) GetLedger(w http.ResponseWriter, r *http.Request) {
	h.ensureQueries()
	uid := currentUserID(r)
	otherID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		httpx.WriteJSON(w, 422, map[string]any{"error": map[string]string{"code": "VALIDATION_ERROR", "message": "invalid user id"}})
		return
	}
	a, b := orderedPair(uid, otherID)
	status, err := h.Queries.GetFriendshipStatus(r.Context(), db.GetFriendshipStatusParams{UserID: a, FriendID: b})
	if err == pgx.ErrNoRows {
		httpx.WriteError(w, r, httpx.ErrNotFound)
		return
	}
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if status != "ACCEPTED" {
		httpx.WriteJSON(w, 403, map[string]any{"error": map[string]string{"code": "FORBIDDEN", "message": "friendship not active"}})
		return
	}
	gid, err := h.ensureDirectGroup(r.Context(), a, b)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	name, _ := h.Queries.GetUserNameByID(r.Context(), otherID)
	httpx.WriteJSON(w, 200, map[string]any{"group_id": gid, "friend_id": otherID, "friend_name": name, "title": "You & " + name})
}
