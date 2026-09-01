package comments

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	db "github.com/settlr-org/settlr-api/internal/db"
	"github.com/settlr-org/settlr-api/internal/httpx"
)

type Handler struct {
	Pool    *pgxpool.Pool
	Queries *db.Queries
}

func (h *Handler) ensureQueries() *db.Queries {
	if h.Queries != nil {
		return h.Queries
	}
	if h.Pool != nil {
		return db.New(h.Pool)
	}
	return nil
}

func (h *Handler) isMember(ctx context.Context, groupID, userID uuid.UUID) bool {
	q := h.ensureQueries()
	ok, err := q.IsMember(ctx, db.IsMemberParams{GroupID: groupID, UserID: userID})
	if err == nil && ok != "" {
		return true
	}
	return false
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
	q := h.ensureQueries()
	groupID, err := q.GetExpenseGroupIDForComments(r.Context(), expenseID)
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
	if !h.isMember(r.Context(), groupID, userID) {
		httpx.WriteError(w, r, httpx.ErrGroupMemberRequired)
		return
	}
	rows, qerr := q.ListComments(r.Context(), expenseID)
	if qerr != nil {
		httpx.WriteError(w, r, qerr)
		return
	}
	var out []map[string]any
	for _, row := range rows {
		out = append(out, map[string]any{"id": row.ID, "user_id": row.UserID, "name": row.Name, "avatar_url": row.AvatarUrl, "body": row.Body, "created_at": row.CreatedAt.Time})
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
	q := h.ensureQueries()
	groupID, err := q.GetExpenseGroupIDForComments(r.Context(), expenseID)
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
	if !h.isMember(r.Context(), groupID, userID) {
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
	err = q.CreateComment(r.Context(), db.CreateCommentParams{ID: id, ExpenseID: expenseID, UserID: userID, Body: req.Body})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	_ = q.CreateCommentActivity(r.Context(), db.CreateCommentActivityParams{GroupID: groupID, ActorID: userID, EntityID: id, Payload: json.RawMessage(`{"expense_id":"` + expenseID.String() + `"}`)})
	// Notify other participants
	nids, _ := q.ListCommentNotifyUsers(r.Context(), db.ListCommentNotifyUsersParams{ExpenseID: expenseID, UserID: userID})
	for _, nid := range nids {
		_ = q.CreateMentionNotification(r.Context(), db.CreateMentionNotificationParams{UserID: nid, Title: "New comment", Body: req.Body, Data: json.RawMessage(`{"expense_id":"` + expenseID.String() + `"}`)})
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
	q := h.ensureQueries()
	owner, err := q.GetCommentOwner(r.Context(), id)
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
	_ = q.SoftDeleteComment(r.Context(), id)
	httpx.WriteJSON(w, 200, map[string]any{"message": "deleted"})
}
