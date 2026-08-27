package search

import (
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nabinkhanal00/settlr-api/internal/httpx"
)

type Handler struct {
	Pool *pgxpool.Pool
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

	// Users
	uRows, _ := h.Pool.Query(r.Context(),
		`SELECT id, name, coalesce(avatar_url,'') FROM users WHERE is_anonymous=false AND (name ILIKE '%' || $1 || '%' OR email ILIKE '%' || $1 || '%') LIMIT 10`, q)
	var users []map[string]any
	if uRows != nil {
		defer uRows.Close()
		for uRows.Next() {
			var id uuid.UUID
			var name, avatar string
			_ = uRows.Scan(&id, &name, &avatar)
			users = append(users, map[string]any{"id": id, "name": name, "avatar_url": avatar})
		}
	}
	if users == nil {
		users = []map[string]any{}
	}

	// Groups where user is member
	gRows, _ := h.Pool.Query(r.Context(),
		`SELECT g.id, g.name FROM groups g JOIN group_members gm ON gm.group_id=g.id WHERE gm.user_id=$1 AND g.archived_at IS NULL AND g.name ILIKE '%' || $2 || '%' LIMIT 10`, userID, q)
	var groups []map[string]any
	if gRows != nil {
		defer gRows.Close()
		for gRows.Next() {
			var id uuid.UUID
			var name string
			_ = gRows.Scan(&id, &name)
			groups = append(groups, map[string]any{"id": id, "name": name})
		}
	}
	if groups == nil {
		groups = []map[string]any{}
	}

	// Expenses in user's groups
	eRows, _ := h.Pool.Query(r.Context(),
		`SELECT e.id, e.group_id, e.description, e.amount, e.currency FROM expenses e
		 WHERE e.deleted_at IS NULL AND e.group_id IN (SELECT group_id FROM group_members WHERE user_id=$1)
		 AND (e.description ILIKE '%' || $2 || '%' OR e.notes ILIKE '%' || $2 || '%') ORDER BY e.created_at DESC LIMIT 10`, userID, q)
	var expenses []map[string]any
	if eRows != nil {
		defer eRows.Close()
		for eRows.Next() {
			var id, gid uuid.UUID
			var desc, currency string
			var amount int64
			_ = eRows.Scan(&id, &gid, &desc, &amount, &currency)
			expenses = append(expenses, map[string]any{"id": id, "group_id": gid, "description": desc, "amount": amount, "currency": currency})
		}
	}
	if expenses == nil {
		expenses = []map[string]any{}
	}

	httpx.WriteJSON(w, 200, map[string]any{"users": users, "groups": groups, "expenses": expenses})
}
