package search

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
	mux.Handle("GET /api/v1/search", authMw(http.HandlerFunc(h.Search)))
}

func (h *Handler) Search(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" {
		httpx.WriteJSON(w, 200, map[string]any{"users": []any{}, "groups": []any{}, "expenses": []any{}})
		return
	}
	uid, _ := httpx.GetUserID(r.Context())
	userID, _ := uuid.Parse(uid)
	queries := h.ensureQueries()
	if queries == nil {
		httpx.WriteJSON(w, 200, map[string]any{"users": []any{}, "groups": []any{}, "expenses": []any{}})
		return
	}
	uRows, _ := queries.SearchUsers(r.Context(), pgtype.Text{String: q, Valid: true})
	var users []map[string]any
	for _, u := range uRows {
		users = append(users, map[string]any{"id": u.ID, "name": u.Name, "avatar_url": u.AvatarUrl})
	}
	if users == nil {
		users = []map[string]any{}
	}
	gRows, _ := queries.SearchGroups(r.Context(), db.SearchGroupsParams{UserID: userID, Column2: pgtype.Text{String: q, Valid: true}})
	var groups []map[string]any
	for _, g := range gRows {
		groups = append(groups, map[string]any{"id": g.ID, "name": g.Name})
	}
	if groups == nil {
		groups = []map[string]any{}
	}
	eRows, _ := queries.SearchExpenses(r.Context(), db.SearchExpensesParams{UserID: userID, Column2: pgtype.Text{String: q, Valid: true}})
	var expenses []map[string]any
	for _, e := range eRows {
		expenses = append(expenses, map[string]any{"id": e.ID, "group_id": e.GroupID, "description": e.Description, "amount": e.Amount, "currency": e.Currency})
	}
	if expenses == nil {
		expenses = []map[string]any{}
	}
	httpx.WriteJSON(w, 200, map[string]any{"users": users, "groups": groups, "expenses": expenses})
}
