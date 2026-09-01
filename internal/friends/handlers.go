package friends

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/settlr-org/settlr-api/internal/auth"
	db "github.com/settlr-org/settlr-api/internal/db"
	"github.com/settlr-org/settlr-api/internal/httpx"
	"github.com/settlr-org/settlr-api/internal/mailer"
)

type Handler struct {
	Pool    *pgxpool.Pool
	Queries *db.Queries
	Mailer  *mailer.Mailer
}

func (h *Handler) ensureQueries() {
	if h.Queries == nil && h.Pool != nil {
		h.Queries = db.New(h.Pool)
	}
}

func (h *Handler) qtx(tx pgx.Tx) *db.Queries {
	if h.Queries != nil {
		return h.Queries.WithTx(tx)
	}
	return db.New(tx)
}

func (h *Handler) RegisterRoutes(mux *http.ServeMux, authMw func(http.Handler) http.Handler) {
	mux.Handle("GET /api/v1/friends", authMw(http.HandlerFunc(h.ListFriends)))
	mux.Handle("POST /api/v1/friends/invite", authMw(http.HandlerFunc(h.InviteByEmail)))
	mux.Handle("POST /api/v1/friend-invites/{token}/accept", authMw(http.HandlerFunc(h.AcceptEmailInvite)))
	mux.Handle("GET /api/v1/friends/{id}/balance", authMw(http.HandlerFunc(h.GetBalance)))
	mux.Handle("POST /api/v1/friends/{id}/request", authMw(http.HandlerFunc(h.SendRequest)))
	mux.Handle("POST /api/v1/friends/{id}/accept", authMw(http.HandlerFunc(h.Accept)))
	mux.Handle("POST /api/v1/friends/{id}/reject", authMw(http.HandlerFunc(h.Reject)))
	mux.Handle("DELETE /api/v1/friends/{id}", authMw(http.HandlerFunc(h.Remove)))
	mux.Handle("POST /api/v1/friends/{id}/block", authMw(http.HandlerFunc(h.Block)))
	mux.Handle("GET /api/v1/friends/requests", authMw(http.HandlerFunc(h.ListRequests)))
	mux.Handle("GET /api/v1/friends/{id}/ledger", authMw(http.HandlerFunc(h.GetLedger)))
}

func (h *Handler) GetBalance(w http.ResponseWriter, r *http.Request) {
	h.ensureQueries()
	uid := currentUserID(r)
	otherID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		httpx.WriteJSON(w, 422, map[string]any{"error": map[string]string{"code": "VALIDATION_ERROR", "message": "invalid user id"}})
		return
	}
	a, b := orderedPair(uid, otherID)
	status, err := h.Queries.GetFriendshipStatus(r.Context(), db.GetFriendshipStatusParams{UserID: a, FriendID: b})
	if err != nil || status != "ACCEPTED" {
		httpx.WriteJSON(w, 403, map[string]any{"error": map[string]string{"code": "FORBIDDEN", "message": "friendship not active"}})
		return
	}
	gid, err := h.ensureDirectGroup(r.Context(), a, b)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	row, err := h.Queries.GetDirectBalance(r.Context(), db.GetDirectBalanceParams{GroupID: gid, PaidBy: uid})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, 200, map[string]any{"amount": row.Amount, "currency": row.Currency})
}

func (h *Handler) InviteByEmail(w http.ResponseWriter, r *http.Request) {
	h.ensureQueries()
	uid := currentUserID(r)
	var req struct {
		Email string `json:"email"`
	}
	if err := httpx.DecodeJSON(r, &req); err != nil || !strings.Contains(strings.TrimSpace(req.Email), "@") {
		httpx.WriteJSON(w, 422, map[string]any{"error": map[string]string{"code": "VALIDATION_ERROR", "message": "valid email required"}})
		return
	}
	email := strings.ToLower(strings.TrimSpace(req.Email))
	target, err := h.Queries.GetFriendUserIDByEmail(r.Context(), email)
	if err == nil {
		if target == uid {
			httpx.WriteJSON(w, 422, map[string]any{"error": map[string]string{"code": "VALIDATION_ERROR", "message": "you cannot invite yourself"}})
			return
		}
		a, b := orderedPair(uid, target)
		err = h.Queries.UpsertFriendshipAccepted(r.Context(), db.UpsertFriendshipAcceptedParams{
			ID:       uuid.New(),
			UserID:   a,
			FriendID: b,
			ActionBy: pgtype.UUID{Bytes: uid, Valid: true},
		})
		if err != nil {
			httpx.WriteError(w, r, err)
			return
		}
		httpx.WriteJSON(w, 201, map[string]any{"status": "ACCEPTED", "user_id": target})
		return
	}
	if err != pgx.ErrNoRows {
		// unexpected error
		if err != nil {
			httpx.WriteError(w, r, err)
			return
		}
	}
	raw, hash := auth.GenerateRefreshToken()
	id := uuid.New()
	err = h.Queries.CreateFriendInvite(r.Context(), db.CreateFriendInviteParams{
		ID:        id,
		Email:     email,
		TokenHash: hash,
		InvitedBy: uid,
	})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	inviter, _ := h.Queries.GetUserNameByID(r.Context(), uid)
	if h.Mailer != nil {
		subject, html := h.Mailer.FriendInviteEmail(email, inviter, raw)
		h.Mailer.SendAsync(email, subject, html)
	}
	httpx.WriteJSON(w, 201, map[string]any{"invite_id": id, "email": email, "expires_at": "7d"})
}

func (h *Handler) AcceptEmailInvite(w http.ResponseWriter, r *http.Request) {
	h.ensureQueries()
	uid := currentUserID(r)
	tx, err := h.Pool.Begin(r.Context())
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	defer tx.Rollback(r.Context())
	qtx := h.qtx(tx)
	row, err := qtx.GetFriendInviteByTokenForUpdate(r.Context(), auth.HashToken(r.PathValue("token")))
	if err == pgx.ErrNoRows {
		httpx.WriteJSON(w, 404, map[string]any{"error": map[string]string{"code": "NOT_FOUND", "message": "invalid or expired friend invite"}})
		return
	}
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if !row.ExpiresAt.Valid || time.Now().After(row.ExpiresAt.Time) {
		httpx.WriteJSON(w, 404, map[string]any{"error": map[string]string{"code": "NOT_FOUND", "message": "invalid or expired friend invite"}})
		return
	}
	if row.Status != "PENDING" {
		httpx.WriteJSON(w, 409, map[string]any{"error": map[string]string{"code": "CONFLICT", "message": "invite has already been accepted"}})
		return
	}
	userEmail, err := qtx.GetUserLowerEmailByID(r.Context(), uid)
	if err != nil {
		// fallback to pool if needed
		userEmail = ""
	}
	if userEmail != strings.ToLower(row.Email) {
		httpx.WriteJSON(w, 403, map[string]any{"error": map[string]string{"code": "FORBIDDEN", "message": "invite email does not match your account"}})
		return
	}
	a, b := orderedPair(uid, row.InvitedBy)
	if err = qtx.UpsertFriendshipAcceptedNoAction(r.Context(), db.UpsertFriendshipAcceptedNoActionParams{
		ID:       uuid.New(),
		UserID:   a,
		FriendID: b,
		ActionBy: pgtype.UUID{Bytes: row.InvitedBy, Valid: true},
	}); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if err = qtx.UpdateFriendInviteAccepted(r.Context(), row.ID); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, 200, map[string]any{"message": "friend invite accepted", "user_id": row.InvitedBy})
}

func currentUserID(r *http.Request) uuid.UUID {
	uid, _ := httpx.GetUserID(r.Context())
	id, _ := uuid.Parse(uid)
	return id
}

func orderedPair(a, b uuid.UUID) (uuid.UUID, uuid.UUID) {
	if a.String() < b.String() {
		return a, b
	}
	return b, a
}

func (h *Handler) ListFriends(w http.ResponseWriter, r *http.Request) {
	h.ensureQueries()
	uid := currentUserID(r)
	rows, err := h.Queries.ListFriends(r.Context(), uid)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	out := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		var otherID uuid.UUID
		if row.OtherID.Valid {
			otherID = uuid.UUID(row.OtherID.Bytes)
		}
		out = append(out, map[string]any{"friendship_id": row.FriendshipID, "user_id": otherID, "name": row.Name, "avatar_url": row.AvatarUrl, "status": row.Status})
	}
	if out == nil {
		out = []map[string]any{}
	}
	httpx.WriteJSON(w, 200, map[string]any{"data": out})
}

func (h *Handler) ListRequests(w http.ResponseWriter, r *http.Request) {
	h.ensureQueries()
	uid := currentUserID(r)
	rows, err := h.Queries.ListFriendRequests(r.Context(), uid)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	out := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		var fromID uuid.UUID
		if row.FromUser.Valid {
			fromID = uuid.UUID(row.FromUser.Bytes)
		}
		out = append(out, map[string]any{"friendship_id": row.FriendshipID, "from_user": fromID, "name": row.Name, "avatar_url": row.AvatarUrl, "created_at": row.CreatedAt.Time})
	}
	if out == nil {
		out = []map[string]any{}
	}
	httpx.WriteJSON(w, 200, map[string]any{"data": out})
}

func (h *Handler) SendRequest(w http.ResponseWriter, r *http.Request) {
	h.ensureQueries()
	uid := currentUserID(r)
	targetID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		httpx.WriteJSON(w, 422, map[string]any{"error": map[string]string{"code": "VALIDATION_ERROR", "message": "invalid user id"}})
		return
	}
	if uid == targetID {
		httpx.WriteJSON(w, 422, map[string]any{"error": map[string]string{"code": "VALIDATION_ERROR", "message": "cannot friend yourself"}})
		return
	}
	exists, err := h.Queries.CheckUserExistsNotAnonymous(r.Context(), targetID)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if !exists {
		httpx.WriteError(w, r, httpx.ErrNotFound)
		return
	}
	a, b := orderedPair(uid, targetID)
	status, err := h.Queries.GetFriendshipStatus(r.Context(), db.GetFriendshipStatusParams{UserID: a, FriendID: b})
	if err == nil {
		if status == "BLOCKED" {
			httpx.WriteJSON(w, 403, map[string]any{"error": map[string]string{"code": "FORBIDDEN", "message": "cannot send request to this user"}})
			return
		}
		httpx.WriteJSON(w, 409, map[string]any{"error": map[string]string{"code": "CONFLICT", "message": "friendship already exists with status " + status}})
		return
	}
	if err != pgx.ErrNoRows {
		httpx.WriteError(w, r, err)
		return
	}
	fid := uuid.New()
	err = h.Queries.SendFriendRequest(r.Context(), db.SendFriendRequestParams{
		ID:       fid,
		UserID:   a,
		FriendID: b,
		ActionBy: pgtype.UUID{Bytes: uid, Valid: true},
	})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	requesterName, _ := h.Queries.GetUserNameByID(r.Context(), uid)
	_ = h.Queries.CreateFriendRequestNotification(r.Context(), db.CreateFriendRequestNotificationParams{
		UserID: targetID,
		Title:  "Friend request",
		Body:   requesterName + " sent you a friend request",
		Data:   json.RawMessage(`{"from_user":"` + uid.String() + `"}`),
	})
	httpx.WriteJSON(w, 201, map[string]any{"friendship_id": fid, "status": "PENDING"})
}

func (h *Handler) Accept(w http.ResponseWriter, r *http.Request) {
	h.ensureQueries()
	uid := currentUserID(r)
	otherID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		httpx.WriteJSON(w, 422, map[string]any{"error": map[string]string{"code": "VALIDATION_ERROR", "message": "invalid user id"}})
		return
	}
	a, b := orderedPair(uid, otherID)
	n, err := h.Queries.AcceptFriendRequest(r.Context(), db.AcceptFriendRequestParams{
		UserID:   a,
		FriendID: b,
		ActionBy: pgtype.UUID{Bytes: uid, Valid: true},
	})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if n == 0 {
		httpx.WriteJSON(w, 404, map[string]any{"error": map[string]string{"code": "NOT_FOUND", "message": "no pending request found"}})
		return
	}
	if _, err := h.ensureDirectGroup(r.Context(), a, b); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, 200, map[string]any{"message": "friend request accepted"})
}

func (h *Handler) Reject(w http.ResponseWriter, r *http.Request) {
	h.ensureQueries()
	uid := currentUserID(r)
	otherID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		httpx.WriteJSON(w, 422, map[string]any{"error": map[string]string{"code": "VALIDATION_ERROR", "message": "invalid user id"}})
		return
	}
	a, b := orderedPair(uid, otherID)
	n, err := h.Queries.RejectFriendRequest(r.Context(), db.RejectFriendRequestParams{
		UserID:   a,
		FriendID: b,
		ActionBy: pgtype.UUID{Bytes: uid, Valid: true},
	})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if n == 0 {
		httpx.WriteJSON(w, 404, map[string]any{"error": map[string]string{"code": "NOT_FOUND", "message": "no pending request"}})
		return
	}
	httpx.WriteJSON(w, 200, map[string]any{"message": "request rejected"})
}

func (h *Handler) Remove(w http.ResponseWriter, r *http.Request) {
	h.ensureQueries()
	uid := currentUserID(r)
	otherID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		httpx.WriteJSON(w, 422, map[string]any{"error": map[string]string{"code": "VALIDATION_ERROR", "message": "invalid user id"}})
		return
	}
	a, b := orderedPair(uid, otherID)
	n, err := h.Queries.DeleteFriendship(r.Context(), db.DeleteFriendshipParams{UserID: a, FriendID: b})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if n == 0 {
		httpx.WriteError(w, r, httpx.ErrNotFound)
		return
	}
	httpx.WriteJSON(w, 200, map[string]any{"message": "friend removed"})
}

func (h *Handler) Block(w http.ResponseWriter, r *http.Request) {
	h.ensureQueries()
	uid := currentUserID(r)
	otherID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		httpx.WriteJSON(w, 422, map[string]any{"error": map[string]string{"code": "VALIDATION_ERROR", "message": "invalid user id"}})
		return
	}
	if uid == otherID {
		httpx.WriteJSON(w, 422, map[string]any{"error": map[string]string{"code": "VALIDATION_ERROR", "message": "cannot block yourself"}})
		return
	}
	a, b := orderedPair(uid, otherID)
	_, _ = h.Queries.DeleteFriendship(r.Context(), db.DeleteFriendshipParams{UserID: a, FriendID: b})
	_ = h.Queries.BlockUser(r.Context(), db.BlockUserParams{
		ID:       uuid.New(),
		UserID:   a,
		FriendID: b,
		ActionBy: pgtype.UUID{Bytes: uid, Valid: true},
	})
	httpx.WriteJSON(w, 200, map[string]any{"message": "user blocked"})
}
