package activity

import (
	"net/http"
	"strconv"

	"github.com/google/uuid"
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
	mux.Handle("GET /api/v1/activity", authMw(http.HandlerFunc(h.Global)))
}

func (h *Handler) Global(w http.ResponseWriter, r *http.Request) {
	uid, _ := httpx.GetUserID(r.Context())
	userID, _ := uuid.Parse(uid)
	query := r.URL.Query()
	limit := 30
	if v, err := strconv.Atoi(query.Get("limit")); err == nil && v > 0 && v <= 100 {
		limit = v
	}
	cursor := query.Get("cursor")
	qry := h.ensureQueries()
	var out []map[string]any
	var lastID *uuid.UUID
	var err error
	if cursor != "" {
		if cid, cerr := uuid.Parse(cursor); cerr == nil {
			rows, qerr := qry.ListGlobalActivityWithCursor(r.Context(), db.ListGlobalActivityWithCursorParams{
				UserID: userID, ID: cid, Limit: int32(limit + 1),
			})
			err = qerr
			if err == nil {
				for _, row := range rows {
					out = append(out, map[string]any{"id": row.ID, "group_id": row.GroupID, "actor_id": row.ActorID, "type": row.Type, "entity_type": row.EntityType, "entity_id": row.EntityID, "payload": row.Payload, "created_at": row.CreatedAt.Time})
					tmp := row.ID
					lastID = &tmp
				}
			}
		} else {
			// invalid cursor -> treat as no cursor
			rows, qerr := qry.ListGlobalActivity(r.Context(), db.ListGlobalActivityParams{UserID: userID, Limit: int32(limit + 1)})
			err = qerr
			if err == nil {
				for _, row := range rows {
					out = append(out, map[string]any{"id": row.ID, "group_id": row.GroupID, "actor_id": row.ActorID, "type": row.Type, "entity_type": row.EntityType, "entity_id": row.EntityID, "payload": row.Payload, "created_at": row.CreatedAt.Time})
					tmp := row.ID
					lastID = &tmp
				}
			}
		}
	} else {
		rows, qerr := qry.ListGlobalActivity(r.Context(), db.ListGlobalActivityParams{UserID: userID, Limit: int32(limit + 1)})
		err = qerr
		if err == nil {
			for _, row := range rows {
				out = append(out, map[string]any{"id": row.ID, "group_id": row.GroupID, "actor_id": row.ActorID, "type": row.Type, "entity_type": row.EntityType, "entity_id": row.EntityID, "payload": row.Payload, "created_at": row.CreatedAt.Time})
				tmp := row.ID
				lastID = &tmp
			}
		}
	}
	if err != nil {
		httpx.WriteError(w, r, err)
		return
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
