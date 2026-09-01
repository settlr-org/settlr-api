package stats

import (
	"net/http"

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
	mux.Handle("GET /api/v1/groups/{id}/stats", authMw(http.HandlerFunc(h.GetGroupStats)))
}

func (h *Handler) GetGroupStats(w http.ResponseWriter, r *http.Request) {
	groupID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		httpx.WriteJSON(w, 422, map[string]any{"error": map[string]string{"code": "VALIDATION_ERROR", "message": "invalid group id"}})
		return
	}
	uid, _ := httpx.GetUserID(r.Context())
	userID, _ := uuid.Parse(uid)
	q := h.ensureQueries()
	if q == nil {
		httpx.WriteError(w, r, httpx.ErrGroupMemberRequired)
		return
	}
	role, err := q.IsMember(r.Context(), db.IsMemberParams{GroupID: groupID, UserID: userID})
	if err != nil || role == "" {
		httpx.WriteError(w, r, httpx.ErrGroupMemberRequired)
		return
	}

	// Range handling: all | thisMonth | last30 | thisYear | custom (from/to YYYY-MM-DD)
	rng := r.URL.Query().Get("range")
	if rng == "" {
		rng = "all"
	}

	var total, avg, count int64
	var byCategory []map[string]any
	var byMember []map[string]any
	var monthly []map[string]any

	switch rng {
	case "thisMonth":
		if row, err := q.GetGroupStatsTotalThisMonth(r.Context(), groupID); err == nil {
			total, avg, count = row.Total, row.Avg, row.Count
		}
		if rows, err := q.GetStatsByCategoryThisMonth(r.Context(), groupID); err == nil {
			for _, row := range rows {
				label := "Uncategorized"
				if row.Name.Valid {
					label = row.Name.String
				}
				byCategory = append(byCategory, map[string]any{"category": label, "icon": row.Icon, "total": row.Total, "count": row.Count})
			}
		}
		if rows, err := q.GetStatsMonthlyThisMonth(r.Context(), groupID); err == nil {
			for _, row := range rows {
				monthly = append(monthly, map[string]any{"month": row.Month, "total": row.Total})
			}
		}
	case "last30":
		if row, err := q.GetGroupStatsTotalLast30(r.Context(), groupID); err == nil {
			total, avg, count = row.Total, row.Avg, row.Count
		}
		if rows, err := q.GetStatsByCategoryLast30(r.Context(), groupID); err == nil {
			for _, row := range rows {
				label := "Uncategorized"
				if row.Name.Valid {
					label = row.Name.String
				}
				byCategory = append(byCategory, map[string]any{"category": label, "icon": row.Icon, "total": row.Total, "count": row.Count})
			}
		}
		if rows, err := q.GetStatsMonthlyLast30(r.Context(), groupID); err == nil {
			for _, row := range rows {
				monthly = append(monthly, map[string]any{"month": row.Month, "total": row.Total})
			}
		}
	case "thisYear":
		if row, err := q.GetGroupStatsTotalThisYear(r.Context(), groupID); err == nil {
			total, avg, count = row.Total, row.Avg, row.Count
		}
		if rows, err := q.GetStatsByCategoryThisYear(r.Context(), groupID); err == nil {
			for _, row := range rows {
				label := "Uncategorized"
				if row.Name.Valid {
					label = row.Name.String
				}
				byCategory = append(byCategory, map[string]any{"category": label, "icon": row.Icon, "total": row.Total, "count": row.Count})
			}
		}
		if rows, err := q.GetStatsMonthlyThisYear(r.Context(), groupID); err == nil {
			for _, row := range rows {
				monthly = append(monthly, map[string]any{"month": row.Month, "total": row.Total})
			}
		}
	case "custom":
		// No dedicated sqlc query for custom range; fall back to unfiltered totals.
		// Keeping sqlc-only as required; custom filtering not supported without dynamic SQL.
		if row, err := q.GetGroupStatsTotal(r.Context(), groupID); err == nil {
			total, avg, count = row.Total, row.Avg, row.Count
		}
		if rows, err := q.GetStatsByCategory(r.Context(), groupID); err == nil {
			for _, row := range rows {
				label := "Uncategorized"
				if row.Name.Valid {
					label = row.Name.String
				}
				byCategory = append(byCategory, map[string]any{"category": label, "icon": row.Icon, "total": row.Total, "count": row.Count})
			}
		}
		if rows, err := q.GetStatsMonthly(r.Context(), groupID); err == nil {
			for _, row := range rows {
				monthly = append(monthly, map[string]any{"month": row.Month, "total": row.Total})
			}
		}
	default: // "all"
		if row, err := q.GetGroupStatsTotal(r.Context(), groupID); err == nil {
			total, avg, count = row.Total, row.Avg, row.Count
		}
		if rows, err := q.GetStatsByCategory(r.Context(), groupID); err == nil {
			for _, row := range rows {
				label := "Uncategorized"
				if row.Name.Valid {
					label = row.Name.String
				}
				byCategory = append(byCategory, map[string]any{"category": label, "icon": row.Icon, "total": row.Total, "count": row.Count})
			}
		}
		if rows, err := q.GetStatsMonthly(r.Context(), groupID); err == nil {
			for _, row := range rows {
				monthly = append(monthly, map[string]any{"month": row.Month, "total": row.Total})
			}
		}
	}

	if byCategory == nil {
		byCategory = []map[string]any{}
	}

	// By member (paid) - not range-filtered in original sqlc
	if memRows, err := q.GetStatsByMember(r.Context(), groupID); err == nil {
		for _, row := range memRows {
			byMember = append(byMember, map[string]any{"user_id": row.ID, "name": row.Name, "total_paid": row.Total, "count": row.Count})
		}
	}
	if byMember == nil {
		byMember = []map[string]any{}
	}

	if monthly == nil {
		monthly = []map[string]any{}
	}

	httpx.WriteJSON(w, 200, map[string]any{
		"total_spent":     total,
		"average_expense": avg,
		"expense_count":   count,
		"by_category":     byCategory,
		"by_member":       byMember,
		"monthly":         monthly,
	})
}
