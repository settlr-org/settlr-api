package wallet

import (
	"errors"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"

	db "github.com/settlr-org/settlr-api/internal/db"
	"github.com/settlr-org/settlr-api/internal/httpx"
)

func tagJSON(t db.Tag) map[string]any {
	return map[string]any{
		"id": t.ID, "name": t.Name, "color": t.Color, "icon": t.Icon, "sort_order": t.SortOrder,
		"created_at": t.CreatedAt.Time.UTC(), "updated_at": t.UpdatedAt.Time.UTC(),
	}
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

func (h *Handler) ListTags(w http.ResponseWriter, r *http.Request) {
	rows, err := h.q().ListTags(r.Context(), currentUser(r))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	out := make([]map[string]any, 0, len(rows))
	for _, t := range rows {
		out = append(out, tagJSON(t))
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"data": out})
}

func (h *Handler) GetTag(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	t, err := h.q().GetTag(r.Context(), db.GetTagParams{ID: id, UserID: currentUser(r)})
	if err != nil {
		httpx.WriteError(w, r, httpx.ErrNotFound)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, tagJSON(t))
}

func (h *Handler) CreateTag(w http.ResponseWriter, r *http.Request) {
	userID := currentUser(r)
	var req struct {
		Name      string `json:"name"`
		Color     string `json:"color"`
		Icon      string `json:"icon"`
		SortOrder *int32 `json:"sort_order"`
	}
	if !decode(w, r, &req) {
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" || len(req.Name) > 50 {
		validation(w, "name required (max 50)")
		return
	}
	if req.Color == "" {
		req.Color = "#6B7280"
	}
	if req.Icon == "" {
		req.Icon = "tag"
	}
	ctx := r.Context()
	var sortOrder int32
	if req.SortOrder != nil {
		sortOrder = *req.SortOrder
	} else {
		var err error
		if sortOrder, err = h.q().NextTagSortOrder(ctx, userID); err != nil {
			httpx.WriteError(w, r, err)
			return
		}
	}
	t, err := h.q().CreateTag(ctx, db.CreateTagParams{UserID: userID, Name: req.Name, Color: req.Color, Icon: req.Icon, SortOrder: sortOrder})
	if isUniqueViolation(err) {
		writeErr(w, http.StatusConflict, "CONFLICT", "a tag with this name already exists")
		return
	}
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, tagJSON(t))
}

func (h *Handler) UpdateTag(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	userID := currentUser(r)
	ctx := r.Context()
	t, err := h.q().GetTag(ctx, db.GetTagParams{ID: id, UserID: userID})
	if err != nil {
		httpx.WriteError(w, r, httpx.ErrNotFound)
		return
	}
	var req struct {
		Name      Opt[string] `json:"name"`
		Color     Opt[string] `json:"color"`
		Icon      Opt[string] `json:"icon"`
		SortOrder Opt[int32]  `json:"sort_order"`
	}
	if !decode(w, r, &req) {
		return
	}
	p := db.UpdateTagParams{ID: id, UserID: userID, Name: t.Name, Color: t.Color, Icon: t.Icon, SortOrder: t.SortOrder}
	if v, ok := req.Name.Value(); ok {
		v = strings.TrimSpace(v)
		if v == "" || len(v) > 50 {
			validation(w, "name required (max 50)")
			return
		}
		p.Name = v
	}
	if v, ok := req.Color.Value(); ok && v != "" {
		p.Color = v
	}
	if v, ok := req.Icon.Value(); ok && v != "" {
		p.Icon = v
	}
	if v, ok := req.SortOrder.Value(); ok {
		p.SortOrder = v
	}
	updated, err := h.q().UpdateTag(ctx, p)
	if isUniqueViolation(err) {
		writeErr(w, http.StatusConflict, "CONFLICT", "a tag with this name already exists")
		return
	}
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, tagJSON(updated))
}

// DeleteTag removes the tag and detaches it from all transactions.
func (h *Handler) DeleteTag(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	n, err := h.q().DeleteTag(r.Context(), db.DeleteTagParams{ID: id, UserID: currentUser(r)})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if n == 0 {
		httpx.WriteError(w, r, httpx.ErrNotFound)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"message": "deleted"})
}
