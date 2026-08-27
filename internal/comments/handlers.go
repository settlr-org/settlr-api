package comments

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nabinkhanal00/settlr-api/internal/httpx"
)

type Handler struct {
	Pool *pgxpool.Pool
}

func (h *Handler) RegisterRoutes(mux *http.ServeMux, authMw func(http.Handler) http.Handler) {
	mux.Handle("GET /api/v1/expenses/{id}/comments", authMw(http.HandlerFunc(h.List)))
	mux.Handle("POST /api/v1/expenses/{id}/comments", authMw(http.HandlerFunc(h.Create)))
	mux.Handle("DELETE /api/v1/comments/{id}", authMw(http.HandlerFunc(h.Delete)))
}

func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	expenseID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		httpx.WriteJSON(w, 422, map[string]any{"error": map[string]string{"code": "VALIDATION_ERROR", "message": "invalid expense id"}})
		return
	}
	// Verify membership via expense group
	var groupID uuid.UUID
	err = h.Pool.QueryRow(r.Context(), `SELECT group_id FROM expenses WHERE id=$1 AND deleted_at IS NULL`, expenseID).Scan(&groupID)
	if err == pgx.ErrNoRows {
		httpx.WriteError(w, r, httpx.ErrNotFound)
		return
	}
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	uid, _ := httpx.GetUserID(r.Context())
	userID, _ := uuid.Parse(uid)
	var ok bool
	_ = h.Pool.QueryRow(r.Context(), `SELECT true FROM group_members WHERE group_id=$1 AND user_id=$2`, groupID, userID).Scan(&ok)
	if !ok {
		httpx.WriteError(w, r, httpx.ErrGroupMemberRequired)
		return
	}
	rows, err := h.Pool.Query(r.Context(),
		`SELECT c.id, c.user_id, u.name, coalesce(u.avatar_url,''), c.body, c.created_at FROM expense_comments c JOIN users u ON u.id=c.user_id WHERE c.expense_id=$1 AND c.deleted_at IS NULL ORDER BY c.created_at ASC`, expenseID)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	defer rows.Close()
	var out []map[string]any
	for rows.Next() {
		var id, uid2 uuid.UUID
		var name, avatar, body string
		var createdAt any
		_ = rows.Scan(&id, &uid2, &name, &avatar, &body, &createdAt)
		out = append(out, map[string]any{"id": id, "user_id": uid2, "name": name, "avatar_url": avatar, "body": body, "created_at": createdAt})
	}
	if out == nil {
		out = []map[string]any{}
	}
	httpx.WriteJSON(w, 200, map[string]any{"data": out})
}

func (h *Handler) Create(w http.ResponseWriter, r *http.Request) {
	expenseID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		httpx.WriteJSON(w, 422, map[string]any{"error": map[string]string{"code": "VALIDATION_ERROR", "message": "invalid expense id"}})
		return
	}
	var groupID uuid.UUID
	err = h.Pool.QueryRow(r.Context(), `SELECT group_id FROM expenses WHERE id=$1 AND deleted_at IS NULL`, expenseID).Scan(&groupID)
	if err == pgx.ErrNoRows {
		httpx.WriteError(w, r, httpx.ErrNotFound)
		return
	}
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	uid, _ := httpx.GetUserID(r.Context())
	userID, _ := uuid.Parse(uid)
	var ok bool
	_ = h.Pool.QueryRow(r.Context(), `SELECT true FROM group_members WHERE group_id=$1 AND user_id=$2`, groupID, userID).Scan(&ok)
	if !ok {
		httpx.WriteError(w, r, httpx.ErrGroupMemberRequired)
		return
	}
	var req struct {
		Body string `json:"body"`
	}
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteJSON(w, 400, map[string]any{"error": map[string]string{"code": "VALIDATION_ERROR", "message": err.Error()}})
		return
	}
	req.Body = strings.TrimSpace(req.Body)
	if req.Body == "" || len(req.Body) > 2000 {
		httpx.WriteJSON(w, 422, map[string]any{"error": map[string]string{"code": "VALIDATION_ERROR", "message": "body 1-2000 chars"}})
		return
	}
	id := uuid.New()
	_, err = h.Pool.Exec(r.Context(), `INSERT INTO expense_comments (id, expense_id, user_id, body) VALUES ($1,$2,$3,$4)`, id, expenseID, userID, req.Body)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	_, _ = h.Pool.Exec(r.Context(), `INSERT INTO activity_events (group_id, actor_id, type, entity_type, entity_id, payload) VALUES ($1,$2,'COMMENT_ADDED','comment',$3,$4)`,
		groupID, userID, id, json.RawMessage(`{"expense_id":"`+expenseID.String()+`"}`))
	// Notify other participants
	rows, _ := h.Pool.Query(r.Context(), `SELECT DISTINCT s.user_id FROM expense_splits s WHERE s.expense_id=$1 AND s.user_id != $2`, expenseID, userID)
	if rows != nil {
		defer rows.Close()
		for rows.Next() {
			var nid uuid.UUID
			_ = rows.Scan(&nid)
			_, _ = h.Pool.Exec(r.Context(), `INSERT INTO notifications (user_id, type, title, body, data) VALUES ($1,'MENTION',$2,$3,$4)`,
				nid, "New comment", req.Body, json.RawMessage(`{"expense_id":"`+expenseID.String()+`"}`))
		}
	}
	httpx.WriteJSON(w, 201, map[string]any{"id": id, "body": req.Body})
}

func (h *Handler) Delete(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		httpx.WriteJSON(w, 422, map[string]any{"error": map[string]string{"code": "VALIDATION_ERROR", "message": "invalid comment id"}})
		return
	}
	uid, _ := httpx.GetUserID(r.Context())
	userID, _ := uuid.Parse(uid)
	var owner uuid.UUID
	err = h.Pool.QueryRow(r.Context(), `SELECT user_id FROM expense_comments WHERE id=$1 AND deleted_at IS NULL`, id).Scan(&owner)
	if err == pgx.ErrNoRows {
		httpx.WriteError(w, r, httpx.ErrNotFound)
		return
	}
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if owner != userID {
		httpx.WriteError(w, r, httpx.ErrForbidden)
		return
	}
	_, _ = h.Pool.Exec(r.Context(), `UPDATE expense_comments SET deleted_at=now(), updated_at=now() WHERE id=$1`, id)
	httpx.WriteJSON(w, 200, map[string]any{"message": "deleted"})
}
