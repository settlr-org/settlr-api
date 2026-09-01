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
	q := h.ensureQueries()
	row, err := q.GetNotificationPreferences(r.Context(), userID)
	if err == pgx.ErrNoRows {
		p := preferences{EmailEnabled: true, PushEnabled: true, FriendRequestEnabled: true, ExpenseEnabled: true, SettlementEnabled: true}
		httpx.WriteJSON(w, http.StatusOK, p)
		return
	}
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	p := preferences{EmailEnabled: row.EmailEnabled, PushEnabled: row.PushEnabled, FriendRequestEnabled: row.FriendRequestEnabled, ExpenseEnabled: row.ExpenseEnabled, SettlementEnabled: row.SettlementEnabled}
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
	q := h.ensureQueries()
	err := q.UpsertNotificationPreferences(r.Context(), db.UpsertNotificationPreferencesParams{
		UserID: userID, EmailEnabled: p.EmailEnabled, PushEnabled: p.PushEnabled,
		FriendRequestEnabled: p.FriendRequestEnabled, ExpenseEnabled: p.ExpenseEnabled, SettlementEnabled: p.SettlementEnabled,
	})
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
	q := h.ensureQueries()
	var out []map[string]any
	var sqlcRows interface{}
	_ = sqlcRows
	if q != nil {
		var rows []db.ListNotificationsRow
		var cursorRows []db.ListNotificationsWithCursorRow
		var err error
		if cursor != "" {
			if cid, cerr := uuid.Parse(cursor); cerr == nil {
				cursorRows, err = q.ListNotificationsWithCursor(r.Context(), db.ListNotificationsWithCursorParams{
					UserID: userID, ID: cid, Limit: int32(limit + 1),
				})
				if err == nil {
					for _, row := range cursorRows {
						var payload any
						if len(row.Data) > 0 {
							_ = json.Unmarshal(row.Data, &payload)
						}
						var readAt *time.Time
						if row.ReadAt.Valid {
							t := row.ReadAt.Time
							readAt = &t
						}
						out = append(out, map[string]any{"id": row.ID, "type": row.Type, "title": row.Title, "body": row.Body, "data": payload, "read_at": readAt, "created_at": row.CreatedAt.Time})
					}
				}
			} else {
				rows, err = q.ListNotifications(r.Context(), db.ListNotificationsParams{UserID: userID, Limit: int32(limit + 1)})
				if err == nil {
					for _, row := range rows {
						var payload any
						if len(row.Data) > 0 {
							_ = json.Unmarshal(row.Data, &payload)
						}
						var readAt *time.Time
						if row.ReadAt.Valid {
							t := row.ReadAt.Time
							readAt = &t
						}
						out = append(out, map[string]any{"id": row.ID, "type": row.Type, "title": row.Title, "body": row.Body, "data": payload, "read_at": readAt, "created_at": row.CreatedAt.Time})
					}
				}
			}
		} else {
			rows, err = q.ListNotifications(r.Context(), db.ListNotificationsParams{UserID: userID, Limit: int32(limit + 1)})
			if err == nil {
				for _, row := range rows {
					var payload any
					if len(row.Data) > 0 {
						_ = json.Unmarshal(row.Data, &payload)
					}
					var readAt *time.Time
					if row.ReadAt.Valid {
						t := row.ReadAt.Time
						readAt = &t
					}
					out = append(out, map[string]any{"id": row.ID, "type": row.Type, "title": row.Title, "body": row.Body, "data": payload, "read_at": readAt, "created_at": row.CreatedAt.Time})
				}
			}
		}
		if err != nil {
			httpx.WriteError(w, r, err)
			return
		}
		// fallback to unread count via sqlc
		unread, _ := q.CountUnreadNotifications(r.Context(), userID)
		var nextCursor *string
		if len(out) > limit {
			out = out[:limit]
			s := out[len(out)-1]["id"].(uuid.UUID).String()
			nextCursor = &s
		}
		if out == nil {
			out = []map[string]any{}
		}
		resp := map[string]any{"data": out, "unread_count": unread}
		if nextCursor != nil {
			resp["next_cursor"] = *nextCursor
		}
		httpx.WriteJSON(w, 200, resp)
		return
	}
	httpx.WriteError(w, r, fmt.Errorf("queries not available"))
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
	q := h.ensureQueries()
	n, err := q.MarkNotificationRead(r.Context(), db.MarkNotificationReadParams{ID: id, UserID: userID})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if n == 0 {
		exists, _ := q.CheckNotificationExists(r.Context(), db.CheckNotificationExistsParams{ID: id, UserID: userID})
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
	q := h.ensureQueries()
	_ = q.MarkAllNotificationsRead(r.Context(), userID)
	httpx.WriteJSON(w, 200, map[string]any{"message": "all marked as read"})
}
