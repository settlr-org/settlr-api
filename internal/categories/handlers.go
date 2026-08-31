package categories

import (
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/settlr-org/settlr-api/internal/httpx"
)

type Handler struct {
	Pool *pgxpool.Pool
}

func (h *Handler) RegisterRoutes(mux *http.ServeMux, authMw func(http.Handler) http.Handler) {
	mux.Handle("GET /api/v1/categories", authMw(http.HandlerFunc(h.List)))
	mux.Handle("POST /api/v1/categories", authMw(http.HandlerFunc(h.Create)))
}

func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	uid, _ := httpx.GetUserID(r.Context())
	userID, _ := uuid.Parse(uid)
	rows, err := h.Pool.Query(r.Context(),
		`SELECT id, name, icon, color, is_system, coalesce(grouping,'Uncategorized') as grouping FROM categories WHERE is_system=true OR owner_id=$1 ORDER BY grouping, is_system DESC, name`, userID)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	defer rows.Close()
	var out []map[string]any
	for rows.Next() {
		var id uuid.UUID
		var name, icon, color, grouping string
		var isSystem bool
		_ = rows.Scan(&id, &name, &icon, &color, &isSystem, &grouping)
		out = append(out, map[string]any{"id": id, "name": name, "icon": icon, "color": color, "is_system": isSystem, "grouping": grouping})
	}
	if out == nil {
		out = []map[string]any{}
	}
	httpx.WriteJSON(w, 200, map[string]any{"data": out})
}

func (h *Handler) Create(w http.ResponseWriter, r *http.Request) {
	uid, _ := httpx.GetUserID(r.Context())
	userID, _ := uuid.Parse(uid)
	var req struct {
		Name     string  `json:"name"`
		Icon     *string `json:"icon"`
		Color    *string `json:"color"`
		Grouping *string `json:"grouping"`
	}
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteJSON(w, 400, map[string]any{"error": map[string]string{"code": "VALIDATION_ERROR", "message": err.Error()}})
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" || len(req.Name) > 50 {
		httpx.WriteJSON(w, 422, map[string]any{"error": map[string]string{"code": "VALIDATION_ERROR", "message": "name required (max 50)"}})
		return
	}
	icon := "tag"
	if req.Icon != nil && *req.Icon != "" {
		icon = *req.Icon
	}
	color := "#6B7280"
	if req.Color != nil && *req.Color != "" {
		color = *req.Color
	}
	grouping := "Uncategorized"
	if req.Grouping != nil && *req.Grouping != "" {
		grouping = *req.Grouping
	}
	id := uuid.New()
	_, err := h.Pool.Exec(r.Context(),
		`INSERT INTO categories (id, name, icon, color, grouping, is_system, owner_id) VALUES ($1,$2,$3,$4,$5,false,$6)`,
		id, req.Name, icon, color, grouping, userID)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, 201, map[string]any{"id": id, "name": req.Name, "icon": icon, "color": color})
}
