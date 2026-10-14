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
	mux.Handle("POST /api/v1/categories/reorder", authMw(http.HandlerFunc(h.Reorder)))
	mux.Handle("PATCH /api/v1/categories/{id}", authMw(http.HandlerFunc(h.Update)))
	mux.Handle("DELETE /api/v1/categories/{id}", authMw(http.HandlerFunc(h.Delete)))
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
		var sortOrder *int32
		if row.SortOrder.Valid {
			v := row.SortOrder.Int32
			sortOrder = &v
		}
		out = append(out, map[string]any{"id": row.ID, "name": row.Name, "icon": row.Icon, "color": row.Color, "is_system": row.IsSystem, "grouping": row.Grouping, "sort_order": sortOrder})
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
	httpx.WriteJSON(w, 201, map[string]any{"id": id, "name": req.Name, "icon": icon, "color": color, "is_system": false, "grouping": grouping, "sort_order": nil})
}

func writeValidation(w http.ResponseWriter, msg string) {
	httpx.WriteJSON(w, 422, map[string]any{"error": map[string]string{"code": "VALIDATION_ERROR", "message": msg}})
}

// Update edits a category the user owns. System categories are immutable (403).
func (h *Handler) Update(w http.ResponseWriter, r *http.Request) {
	uid, _ := httpx.GetUserID(r.Context())
	userID, _ := uuid.Parse(uid)
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeValidation(w, "invalid id")
		return
	}
	q := h.ensureQueries()
	cur, err := q.GetVisibleCategory(r.Context(), db.GetVisibleCategoryParams{ID: id, UserID: userID})
	if err != nil {
		httpx.WriteError(w, r, httpx.ErrNotFound)
		return
	}
	if cur.IsSystem {
		httpx.WriteJSON(w, 403, map[string]any{"error": map[string]string{"code": "FORBIDDEN", "message": "system categories cannot be changed"}})
		return
	}
	var req struct {
		Name     *string `json:"name"`
		Icon     *string `json:"icon"`
		Color    *string `json:"color"`
		Grouping *string `json:"grouping"`
	}
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteJSON(w, 400, map[string]any{"error": map[string]string{"code": "VALIDATION_ERROR", "message": err.Error()}})
		return
	}
	p := db.UpdateCategoryParams{ID: id, UserID: userID, Name: cur.Name, Icon: cur.Icon, Color: cur.Color,
		Grouping: pgtype.Text{String: cur.Grouping, Valid: true}}
	if req.Name != nil {
		n := strings.TrimSpace(*req.Name)
		if n == "" || len(n) > 50 {
			writeValidation(w, "name required (max 50)")
			return
		}
		p.Name = n
	}
	if req.Icon != nil && *req.Icon != "" {
		p.Icon = *req.Icon
	}
	if req.Color != nil && *req.Color != "" {
		p.Color = *req.Color
	}
	if req.Grouping != nil && *req.Grouping != "" {
		p.Grouping = pgtype.Text{String: *req.Grouping, Valid: true}
	}
	row, err := q.UpdateCategory(r.Context(), p)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, 200, map[string]any{"id": row.ID, "name": row.Name, "icon": row.Icon, "color": row.Color, "is_system": row.IsSystem, "grouping": row.Grouping})
}

// Delete removes a category the user owns; transactions and expenses using it
// become uncategorized. System categories cannot be deleted (403).
func (h *Handler) Delete(w http.ResponseWriter, r *http.Request) {
	uid, _ := httpx.GetUserID(r.Context())
	userID, _ := uuid.Parse(uid)
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeValidation(w, "invalid id")
		return
	}
	q := h.ensureQueries()
	cur, err := q.GetVisibleCategory(r.Context(), db.GetVisibleCategoryParams{ID: id, UserID: userID})
	if err != nil {
		httpx.WriteError(w, r, httpx.ErrNotFound)
		return
	}
	if cur.IsSystem {
		httpx.WriteJSON(w, 403, map[string]any{"error": map[string]string{"code": "FORBIDDEN", "message": "system categories cannot be deleted"}})
		return
	}
	n, err := q.DeleteCategory(r.Context(), db.DeleteCategoryParams{ID: id, UserID: userID})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if n == 0 {
		httpx.WriteError(w, r, httpx.ErrNotFound)
		return
	}
	httpx.WriteJSON(w, 200, map[string]any{"message": "deleted"})
}

// Reorder stores the user's order for the given visible categories (system ones
// included); categories not listed keep the default order after them.
func (h *Handler) Reorder(w http.ResponseWriter, r *http.Request) {
	uid, _ := httpx.GetUserID(r.Context())
	userID, _ := uuid.Parse(uid)
	var req struct {
		IDs []uuid.UUID `json:"ids"`
	}
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteJSON(w, 400, map[string]any{"error": map[string]string{"code": "VALIDATION_ERROR", "message": err.Error()}})
		return
	}
	seen := map[uuid.UUID]bool{}
	ids := make([]pgtype.UUID, 0, len(req.IDs))
	for _, id := range req.IDs {
		if seen[id] {
			writeValidation(w, "ids must be distinct")
			return
		}
		seen[id] = true
		ids = append(ids, pgtype.UUID{Bytes: id, Valid: true})
	}
	if len(ids) == 0 {
		writeValidation(w, "ids required")
		return
	}
	q := h.ensureQueries()
	n, err := q.CountVisibleCategories(r.Context(), db.CountVisibleCategoriesParams{Ids: ids, UserID: pgtype.UUID{Bytes: userID, Valid: true}})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if int(n) != len(ids) {
		writeValidation(w, "ids contains an unknown category")
		return
	}
	tx, err := h.Pool.Begin(r.Context())
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	defer func() { _ = tx.Rollback(r.Context()) }()
	qtx := q.WithTx(tx)
	for i, id := range req.IDs {
		if err := qtx.UpsertCategoryOrder(r.Context(), db.UpsertCategoryOrderParams{UserID: userID, CategoryID: id, SortOrder: int32(i)}); err != nil {
			httpx.WriteError(w, r, err)
			return
		}
	}
	if err := tx.Commit(r.Context()); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	h.List(w, r)
}
