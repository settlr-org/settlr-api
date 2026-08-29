package friends

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nabinkhanal00/settlr-api/internal/auth"
	"github.com/nabinkhanal00/settlr-api/internal/httpx"
	"github.com/nabinkhanal00/settlr-api/internal/mailer"
)

type Handler struct {
	Pool   *pgxpool.Pool
	Mailer *mailer.Mailer
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
	uid := currentUserID(r)
	otherID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		httpx.WriteJSON(w, 422, map[string]any{"error": map[string]string{"code": "VALIDATION_ERROR", "message": "invalid user id"}})
		return
	}
	a, b := orderedPair(uid, otherID)
	var status string
	if err := h.Pool.QueryRow(r.Context(), `SELECT status FROM friendships WHERE user_id=$1 AND friend_id=$2`, a, b).Scan(&status); err != nil || status != "ACCEPTED" {
		httpx.WriteJSON(w, 403, map[string]any{"error": map[string]string{"code": "FORBIDDEN", "message": "friendship not active"}})
		return
	}
	gid, err := ensureDirectGroup(r.Context(), h.Pool, a, b)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	var amount int64
	var currency string
	err = h.Pool.QueryRow(r.Context(), `
		SELECT COALESCE((SELECT SUM(ROUND(amount * COALESCE(exchange_rate, 1))::bigint) FROM expenses WHERE group_id=$1 AND paid_by=$2 AND deleted_at IS NULL),0)
		 - COALESCE((SELECT SUM(ROUND(s.amount * COALESCE(e.exchange_rate, 1))::bigint) FROM expense_splits s JOIN expenses e ON e.id=s.expense_id WHERE e.group_id=$1 AND s.user_id=$2 AND e.deleted_at IS NULL),0)
		 + COALESCE((SELECT SUM(amount) FROM settlements WHERE group_id=$1 AND from_user=$2 AND deleted_at IS NULL),0)
		 - COALESCE((SELECT SUM(amount) FROM settlements WHERE group_id=$1 AND to_user=$2 AND deleted_at IS NULL),0),
		 (SELECT currency FROM groups WHERE id=$1)`, gid, uid).Scan(&amount, &currency)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, 200, map[string]any{"amount": amount, "currency": currency})
}

func (h *Handler) InviteByEmail(w http.ResponseWriter, r *http.Request) {
	uid := currentUserID(r)
	var req struct {
		Email string `json:"email"`
	}
	if err := httpx.DecodeJSON(r, &req); err != nil || !strings.Contains(strings.TrimSpace(req.Email), "@") {
		httpx.WriteJSON(w, 422, map[string]any{"error": map[string]string{"code": "VALIDATION_ERROR", "message": "valid email required"}})
		return
	}
	email := strings.ToLower(strings.TrimSpace(req.Email))
	var target uuid.UUID
	if err := h.Pool.QueryRow(r.Context(), `SELECT id FROM users WHERE lower(email)=lower($1)`, email).Scan(&target); err == nil {
		if target == uid {
			httpx.WriteJSON(w, 422, map[string]any{"error": map[string]string{"code": "VALIDATION_ERROR", "message": "you cannot invite yourself"}})
			return
		}
		a, b := orderedPair(uid, target)
		_, err = h.Pool.Exec(r.Context(), `INSERT INTO friendships (id, user_id, friend_id, status, action_by) VALUES ($1,$2,$3,'ACCEPTED',$4) ON CONFLICT DO UPDATE SET status='ACCEPTED', action_by=$4, updated_at=now()`, uuid.New(), a, b, uid)
		if err != nil {
			httpx.WriteError(w, r, err)
			return
		}
		httpx.WriteJSON(w, 201, map[string]any{"status": "ACCEPTED", "user_id": target})
		return
	}
	raw, hash := auth.GenerateRefreshToken()
	id := uuid.New()
	_, err := h.Pool.Exec(r.Context(), `INSERT INTO friend_invites (id, email, token_hash, invited_by) VALUES ($1,$2,$3,$4) ON CONFLICT (lower(email), invited_by) WHERE status='PENDING' DO UPDATE SET token_hash=$3, created_at=now(), expires_at=now()+interval '7 days'`, id, email, hash, uid)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	var inviter string
	_ = h.Pool.QueryRow(r.Context(), `SELECT name FROM users WHERE id=$1`, uid).Scan(&inviter)
	if h.Mailer != nil {
		subject, html := h.Mailer.FriendInviteEmail(email, inviter, raw)
		h.Mailer.SendAsync(email, subject, html)
	}
	httpx.WriteJSON(w, 201, map[string]any{"invite_id": id, "email": email, "expires_at": "7d"})
}

func (h *Handler) AcceptEmailInvite(w http.ResponseWriter, r *http.Request) {
	uid := currentUserID(r)
	tx, err := h.Pool.Begin(r.Context())
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	defer tx.Rollback(r.Context())
	var id, inviter uuid.UUID
	var email, status string
	var expires time.Time
	err = tx.QueryRow(r.Context(), `SELECT id, email, invited_by, status, expires_at FROM friend_invites WHERE token_hash=$1 FOR UPDATE`, auth.HashToken(r.PathValue("token"))).Scan(&id, &email, &inviter, &status, &expires)
	if err == pgx.ErrNoRows || time.Now().After(expires) {
		httpx.WriteJSON(w, 404, map[string]any{"error": map[string]string{"code": "NOT_FOUND", "message": "invalid or expired friend invite"}})
		return
	}
	if err != nil || status != "PENDING" {
		httpx.WriteJSON(w, 409, map[string]any{"error": map[string]string{"code": "CONFLICT", "message": "invite has already been accepted"}})
		return
	}
	var userEmail string
	_ = tx.QueryRow(r.Context(), `SELECT lower(email) FROM users WHERE id=$1`, uid).Scan(&userEmail)
	if userEmail != strings.ToLower(email) {
		httpx.WriteJSON(w, 403, map[string]any{"error": map[string]string{"code": "FORBIDDEN", "message": "invite email does not match your account"}})
		return
	}
	a, b := orderedPair(uid, inviter)
	if _, err = tx.Exec(r.Context(), `INSERT INTO friendships (id, user_id, friend_id, status, action_by) VALUES ($1,$2,$3,'ACCEPTED',$4) ON CONFLICT DO UPDATE SET status='ACCEPTED', updated_at=now()`, uuid.New(), a, b, inviter); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if _, err = tx.Exec(r.Context(), `UPDATE friend_invites SET status='ACCEPTED' WHERE id=$1`, id); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, 200, map[string]any{"message": "friend invite accepted", "user_id": inviter})
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
	uid := currentUserID(r)
	rows, err := h.Pool.Query(r.Context(), `
		SELECT f.id, f.status,
		       CASE WHEN f.user_id=$1 THEN f.friend_id ELSE f.user_id END AS other_id,
		       u.name, coalesce(u.avatar_url,'')
		FROM friendships f
		JOIN users u ON u.id = CASE WHEN f.user_id=$1 THEN f.friend_id ELSE f.user_id END
		WHERE (f.user_id=$1 OR f.friend_id=$1) AND f.status='ACCEPTED'`, uid)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	defer rows.Close()
	var out []map[string]any
	for rows.Next() {
		var fid uuid.UUID
		var status string
		var otherID uuid.UUID
		var name, avatar string
		_ = rows.Scan(&fid, &status, &otherID, &name, &avatar)
		out = append(out, map[string]any{"friendship_id": fid, "user_id": otherID, "name": name, "avatar_url": avatar, "status": status})
	}
	if out == nil {
		out = []map[string]any{}
	}
	httpx.WriteJSON(w, 200, map[string]any{"data": out})
}

func (h *Handler) ListRequests(w http.ResponseWriter, r *http.Request) {
	uid := currentUserID(r)
	rows, err := h.Pool.Query(r.Context(), `
		SELECT f.id, f.action_by, u.name, coalesce(u.avatar_url,''), f.created_at
		FROM friendships f JOIN users u ON u.id=f.action_by
		WHERE (f.user_id=$1 OR f.friend_id=$1) AND f.status='PENDING' AND f.action_by != $1 ORDER BY f.created_at DESC`, uid)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	defer rows.Close()
	var out []map[string]any
	for rows.Next() {
		var fid, fromID uuid.UUID
		var name, avatar string
		var createdAt any
		_ = rows.Scan(&fid, &fromID, &name, &avatar, &createdAt)
		out = append(out, map[string]any{"friendship_id": fid, "from_user": fromID, "name": name, "avatar_url": avatar, "created_at": createdAt})
	}
	if out == nil {
		out = []map[string]any{}
	}
	httpx.WriteJSON(w, 200, map[string]any{"data": out})
}

func (h *Handler) SendRequest(w http.ResponseWriter, r *http.Request) {
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
	// Check target exists
	var exists bool
	_ = h.Pool.QueryRow(r.Context(), `SELECT true FROM users WHERE id=$1 AND is_anonymous=false`, targetID).Scan(&exists)
	if !exists {
		httpx.WriteError(w, r, httpx.ErrNotFound)
		return
	}
	a, b := orderedPair(uid, targetID)
	// Check existing friendship
	var status string
	err = h.Pool.QueryRow(r.Context(), `SELECT status FROM friendships WHERE user_id=$1 AND friend_id=$2`, a, b).Scan(&status)
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
	// Store with ordered pair; remember requester via action_by and also store raw direction in data
	_, err = h.Pool.Exec(r.Context(),
		`INSERT INTO friendships (id, user_id, friend_id, status, action_by) VALUES ($1,$2,$3,'PENDING',$4)`, fid, a, b, uid)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	// Also track who initiated: store in friendships via action_by already; use notification
	var requesterName string
	_ = h.Pool.QueryRow(r.Context(), `SELECT name FROM users WHERE id=$1`, uid).Scan(&requesterName)
	_, _ = h.Pool.Exec(r.Context(),
		`INSERT INTO notifications (user_id, type, title, body, data) VALUES ($1,'FRIEND_REQUEST',$2,$3,$4)`,
		targetID, "Friend request", requesterName+" sent you a friend request", json.RawMessage(`{"from_user":"`+uid.String()+`"}`))
	httpx.WriteJSON(w, 201, map[string]any{"friendship_id": fid, "status": "PENDING"})
}

func (h *Handler) Accept(w http.ResponseWriter, r *http.Request) {
	uid := currentUserID(r)
	otherID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		httpx.WriteJSON(w, 422, map[string]any{"error": map[string]string{"code": "VALIDATION_ERROR", "message": "invalid user id"}})
		return
	}
	a, b := orderedPair(uid, otherID)
	res, err := h.Pool.Exec(r.Context(),
		`UPDATE friendships SET status='ACCEPTED', updated_at=now() WHERE user_id=$1 AND friend_id=$2 AND status='PENDING' AND action_by != $3`, a, b, uid)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if res.RowsAffected() == 0 {
		httpx.WriteJSON(w, 404, map[string]any{"error": map[string]string{"code": "NOT_FOUND", "message": "no pending request found"}})
		return
	}
	// Create the shared 1:1 ledger eagerly so it shows up in both balances
	if _, err := ensureDirectGroup(r.Context(), h.Pool, a, b); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, 200, map[string]any{"message": "friend request accepted"})
}

func (h *Handler) Reject(w http.ResponseWriter, r *http.Request) {
	uid := currentUserID(r)
	otherID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		httpx.WriteJSON(w, 422, map[string]any{"error": map[string]string{"code": "VALIDATION_ERROR", "message": "invalid user id"}})
		return
	}
	a, b := orderedPair(uid, otherID)
	res, _ := h.Pool.Exec(r.Context(), `DELETE FROM friendships WHERE user_id=$1 AND friend_id=$2 AND status='PENDING' AND action_by != $3`, a, b, uid)
	if res.RowsAffected() == 0 {
		httpx.WriteJSON(w, 404, map[string]any{"error": map[string]string{"code": "NOT_FOUND", "message": "no pending request"}})
		return
	}
	httpx.WriteJSON(w, 200, map[string]any{"message": "request rejected"})
}

func (h *Handler) Remove(w http.ResponseWriter, r *http.Request) {
	uid := currentUserID(r)
	otherID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		httpx.WriteJSON(w, 422, map[string]any{"error": map[string]string{"code": "VALIDATION_ERROR", "message": "invalid user id"}})
		return
	}
	a, b := orderedPair(uid, otherID)
	res, _ := h.Pool.Exec(r.Context(), `DELETE FROM friendships WHERE user_id=$1 AND friend_id=$2`, a, b)
	if res.RowsAffected() == 0 {
		httpx.WriteError(w, r, httpx.ErrNotFound)
		return
	}
	httpx.WriteJSON(w, 200, map[string]any{"message": "friend removed"})
}

func (h *Handler) Block(w http.ResponseWriter, r *http.Request) {
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
	_, _ = h.Pool.Exec(r.Context(), `DELETE FROM friendships WHERE user_id=$1 AND friend_id=$2`, a, b)
	_, _ = h.Pool.Exec(r.Context(),
		`INSERT INTO friendships (id, user_id, friend_id, status, action_by) VALUES ($1,$2,$3,'BLOCKED',$4)`,
		uuid.New(), a, b, uid)
	httpx.WriteJSON(w, 200, map[string]any{"message": "user blocked"})
}
