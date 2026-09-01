package categories

import (
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
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
	mux.Handle("GET /api/v1/categories", authMw(http.HandlerFunc(h.List)))
	mux.Handle("POST /api/v1/categories", authMw(http.HandlerFunc(h.Create)))
}

func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	uid, _ := httpx.GetUserID(r.Context())
	userID, _ := uuid.Parse(uid)
	q := h.ensureQueries()
	rows, err := q.ListCategories(r.Context(), userID)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	var out []map[string]any
	for _, row := range rows {
		out = append(out, map[string]any{"id": row.ID, "name": row.Name, "icon": row.Icon, "color": row.Color, "is_system": row.IsSystem, "grouping": row.Grouping})
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
	q := h.ensureQueries()
	err := q.CreateCategory(r.Context(), db.CreateCategoryParams{
		ID:       id,
		Name:     req.Name,
		Icon:     icon,
		Color:    color,
		Grouping: pgtype.Text{String: grouping, Valid: true},
		OwnerID:  userID,
	})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, 201, map[string]any{"id": id, "name": req.Name, "icon": icon, "color": color})
}
