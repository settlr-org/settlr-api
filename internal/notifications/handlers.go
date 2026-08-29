package notifications

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nabinkhanal00/settlr-api/internal/httpx"
)

type Handler struct {
	Pool *pgxpool.Pool
}

func (h *Handler) RegisterRoutes(mux *http.ServeMux, authMw func(http.Handler) http.Handler) {
	mux.Handle("GET /api/v1/notifications", authMw(http.HandlerFunc(h.List)))
	mux.Handle("POST /api/v1/notifications/{id}/read", authMw(http.HandlerFunc(h.MarkRead)))
	mux.Handle("POST /api/v1/notifications/read-all", authMw(http.HandlerFunc(h.MarkAllRead)))
	mux.Handle("GET /api/v1/me/notification-preferences", authMw(http.HandlerFunc(h.GetPreferences)))
	mux.Handle("PATCH /api/v1/me/notification-preferences", authMw(http.HandlerFunc(h.UpdatePreferences)))
}

type preferences struct {
	EmailEnabled         bool `json:"email_enabled"`
	PushEnabled          bool `json:"push_enabled"`
	FriendRequestEnabled bool `json:"friend_request_enabled"`
	ExpenseEnabled       bool `json:"expense_enabled"`
	SettlementEnabled    bool `json:"settlement_enabled"`
}

func (h *Handler) GetPreferences(w http.ResponseWriter, r *http.Request) {
	uid, _ := httpx.GetUserID(r.Context())
	userID, _ := uuid.Parse(uid)
	var p preferences
	err := h.Pool.QueryRow(r.Context(), `
		SELECT email_enabled, push_enabled, friend_request_enabled, expense_enabled, settlement_enabled
		FROM notification_preferences WHERE user_id=$1`, userID).
		Scan(&p.EmailEnabled, &p.PushEnabled, &p.FriendRequestEnabled, &p.ExpenseEnabled, &p.SettlementEnabled)
	if err == pgx.ErrNoRows {
		p = preferences{EmailEnabled: true, PushEnabled: true, FriendRequestEnabled: true, ExpenseEnabled: true, SettlementEnabled: true}
	} else if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, p)
}

func (h *Handler) UpdatePreferences(w http.ResponseWriter, r *http.Request) {
	uid, _ := httpx.GetUserID(r.Context())
	userID, _ := uuid.Parse(uid)
	var p preferences
	if err := httpx.DecodeJSON(r, &p); err != nil {
		httpx.WriteJSON(w, http.StatusBadRequest, map[string]any{"error": map[string]string{"code": "VALIDATION_ERROR", "message": err.Error()}})
		return
	}
	_, err := h.Pool.Exec(r.Context(), `
		INSERT INTO notification_preferences (user_id, email_enabled, push_enabled, friend_request_enabled, expense_enabled, settlement_enabled)
		VALUES ($1,$2,$3,$4,$5,$6)
		ON CONFLICT (user_id) DO UPDATE SET email_enabled=EXCLUDED.email_enabled, push_enabled=EXCLUDED.push_enabled,
		friend_request_enabled=EXCLUDED.friend_request_enabled, expense_enabled=EXCLUDED.expense_enabled,
		settlement_enabled=EXCLUDED.settlement_enabled`, userID, p.EmailEnabled, p.PushEnabled, p.FriendRequestEnabled, p.ExpenseEnabled, p.SettlementEnabled)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, p)
}

func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	uid, _ := httpx.GetUserID(r.Context())
	userID, _ := uuid.Parse(uid)
	limit := 30
	if v, err := parseLimit(r.URL.Query().Get("limit")); err == nil {
		limit = v
	}
	cursor := r.URL.Query().Get("cursor")
	where := `user_id=$1`
	args := []any{userID}
	idx := 2
	if cursor != "" {
		if cid, err := uuid.Parse(cursor); err == nil {
			where += ` AND (created_at, id) < (SELECT created_at, id FROM notifications WHERE id=$` + itoa(idx) + `)`
			args = append(args, cid)
			idx++
		}
	}
	query := `SELECT id, type, title, body, data, read_at, created_at FROM notifications WHERE ` + where + ` ORDER BY created_at DESC, id DESC LIMIT $` + itoa(idx)
	args = append(args, limit+1)
	rows, err := h.Pool.Query(r.Context(), query, args...)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	defer rows.Close()
	var out []map[string]any
	for rows.Next() {
		var id uuid.UUID
		var typ, title, body string
		var data []byte
		var readAt *time.Time
		var createdAt time.Time
		if err := rows.Scan(&id, &typ, &title, &body, &data, &readAt, &createdAt); err != nil {
			httpx.WriteError(w, r, err)
			return
		}
		var payload any
		if len(data) > 0 {
			_ = json.Unmarshal(data, &payload)
		}
		out = append(out, map[string]any{"id": id, "type": typ, "title": title, "body": body, "data": payload, "read_at": readAt, "created_at": createdAt})
	}
	var nextCursor *string
	if len(out) > limit {
		out = out[:limit]
		s := out[len(out)-1]["id"].(uuid.UUID).String()
		nextCursor = &s
	}
	if out == nil {
		out = []map[string]any{}
	}
	var unread int
	_ = h.Pool.QueryRow(r.Context(), `SELECT count(*) FROM notifications WHERE user_id=$1 AND read_at IS NULL`, userID).Scan(&unread)
	resp := map[string]any{"data": out, "unread_count": unread}
	if nextCursor != nil {
		resp["next_cursor"] = *nextCursor
	}
	httpx.WriteJSON(w, 200, resp)
}

func parseLimit(s string) (int, error) {
	if s == "" {
		return 30, nil
	}
	var v int
	_, err := fmt.Sscanf(s, "%d", &v)
	if err != nil || v <= 0 || v > 100 {
		return 30, err
	}
	return v, nil
}
func itoa(i int) string { return strconv.Itoa(i) }

func (h *Handler) MarkRead(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		httpx.WriteJSON(w, 422, map[string]any{"error": map[string]string{"code": "VALIDATION_ERROR", "message": "invalid notification id"}})
		return
	}
	uid, _ := httpx.GetUserID(r.Context())
	userID, _ := uuid.Parse(uid)
	res, err := h.Pool.Exec(r.Context(), `UPDATE notifications SET read_at=now() WHERE id=$1 AND user_id=$2 AND read_at IS NULL`, id, userID)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if res.RowsAffected() == 0 {
		// Check if exists but already read
		var exists bool
		_ = h.Pool.QueryRow(r.Context(), `SELECT true FROM notifications WHERE id=$1 AND user_id=$2`, id, userID).Scan(&exists)
		if !exists {
			httpx.WriteError(w, r, httpx.ErrNotFound)
			return
		}
	}
	httpx.WriteJSON(w, 200, map[string]any{"message": "marked as read"})
}

func (h *Handler) MarkAllRead(w http.ResponseWriter, r *http.Request) {
	uid, _ := httpx.GetUserID(r.Context())
	userID, _ := uuid.Parse(uid)
	_, _ = h.Pool.Exec(r.Context(), `UPDATE notifications SET read_at=now() WHERE user_id=$1 AND read_at IS NULL`, userID)
	httpx.WriteJSON(w, 200, map[string]any{"message": "all marked as read"})
}
