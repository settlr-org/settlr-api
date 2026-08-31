package activity

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/settlr-org/settlr-api/internal/httpx"
)

type Handler struct {
	Pool *pgxpool.Pool
}

func (h *Handler) RegisterRoutes(mux *http.ServeMux, authMw func(http.Handler) http.Handler) {
	mux.Handle("GET /api/v1/activity", authMw(http.HandlerFunc(h.Global)))
}

func (h *Handler) Global(w http.ResponseWriter, r *http.Request) {
	uid, _ := httpx.GetUserID(r.Context())
	userID, _ := uuid.Parse(uid)
	q := r.URL.Query()
	limit := 30
	if v, err := strconv.Atoi(q.Get("limit")); err == nil && v > 0 && v <= 100 {
		limit = v
	}
	cursor := q.Get("cursor")
	// Only activity for groups user is member of
	where := `a.group_id IN (SELECT group_id FROM group_members WHERE user_id=$1)`
	args := []any{userID}
	idx := 2
	if cursor != "" {
		if cid, err := uuid.Parse(cursor); err == nil {
			where += ` AND (a.created_at, a.id) < (SELECT created_at, id FROM activity_events WHERE id=$` + strconv.Itoa(idx) + `)`
			args = append(args, cid)
			idx++
		}
	}
	query := `SELECT a.id, a.group_id, a.actor_id, a.type, a.entity_type, a.entity_id, a.payload, a.created_at
			  FROM activity_events a WHERE ` + where + ` ORDER BY a.created_at DESC, a.id DESC LIMIT $` + strconv.Itoa(idx)
	args = append(args, limit+1)
	rows, err := h.Pool.Query(r.Context(), query, args...)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	defer rows.Close()
	var out []map[string]any
	var lastID *uuid.UUID
	for rows.Next() {
		var id, groupID, entityID *uuid.UUID
		var actorID *uuid.UUID
		var typ, entityType string
		var payload json.RawMessage
		var createdAt any
		_ = rows.Scan(&id, &groupID, &actorID, &typ, &entityType, &entityID, &payload, &createdAt)
		out = append(out, map[string]any{"id": id, "group_id": groupID, "actor_id": actorID, "type": typ, "entity_type": entityType, "entity_id": entityID, "payload": payload, "created_at": createdAt})
		tmp := *id
		lastID = &tmp
	}
	if out == nil {
		out = []map[string]any{}
	}
	var nextCursor *string
	if len(out) > limit {
		out = out[:limit]
		if lastID != nil {
			s := lastID.String()
			nextCursor = &s
		}
	}
	resp := map[string]any{"data": out}
	if nextCursor != nil {
		resp["next_cursor"] = *nextCursor
	}
	httpx.WriteJSON(w, 200, resp)
}
