package stats

import (
	"net/http"
	"strconv"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nabinkhanal00/settlr-api/internal/httpx"
)

type Handler struct {
	Pool *pgxpool.Pool
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
	var ok bool
	_ = h.Pool.QueryRow(r.Context(), `SELECT true FROM group_members WHERE group_id=$1 AND user_id=$2`, groupID, userID).Scan(&ok)
	if !ok {
		httpx.WriteError(w, r, httpx.ErrGroupMemberRequired)
		return
	}

	// Range handling: all | thisMonth | last30 | thisYear | custom (from/to YYYY-MM-DD)
	q := r.URL.Query()
	rng := q.Get("range")
	if rng == "" {
		rng = "all"
	}
	dateFilter := ""
	var dateArgs []any
	argIdx := 2
	switch rng {
	case "thisMonth":
		dateFilter = ` AND expense_date >= date_trunc('month', CURRENT_DATE)`
	case "last30":
		dateFilter = ` AND expense_date >= CURRENT_DATE - interval '30 days'`
	case "thisYear":
		dateFilter = ` AND expense_date >= date_trunc('year', CURRENT_DATE)`
	case "custom":
		from := q.Get("from")
		to := q.Get("to")
		if from != "" {
			dateFilter += ` AND expense_date >= $` + strconv.Itoa(argIdx)
			dateArgs = append(dateArgs, from)
			argIdx++
		}
		if to != "" {
			dateFilter += ` AND expense_date <= $` + strconv.Itoa(argIdx)
			dateArgs = append(dateArgs, to)
			argIdx++
		}
	}

	var total, avg, count int64
	query := `SELECT coalesce(sum(amount * COALESCE(exchange_rate,1)),0)::bigint, coalesce(round(avg(amount * COALESCE(exchange_rate,1))),0)::bigint, count(*) FROM expenses WHERE group_id=$1 AND deleted_at IS NULL` + dateFilter
	args := []any{groupID}
	args = append(args, dateArgs...)
	_ = h.Pool.QueryRow(r.Context(), query, args...).Scan(&total, &avg, &count)

	// By category (with range filter)
	catQuery := `SELECT c.name, coalesce(c.icon,'tag'), coalesce(sum(ROUND(e.amount * COALESCE(e.exchange_rate,1))::bigint),0) AS total, count(*)
		FROM expenses e LEFT JOIN categories c ON c.id=e.category_id
		WHERE e.group_id=$1 AND e.deleted_at IS NULL` + dateFilter + ` GROUP BY c.name, c.icon ORDER BY total DESC`
	catArgs := []any{groupID}
	catArgs = append(catArgs, dateArgs...)
	catRows, _ := h.Pool.Query(r.Context(), catQuery, catArgs...)
	var byCategory []map[string]any
	if catRows != nil {
		defer catRows.Close()
		for catRows.Next() {
			var name, icon *string
			var t int64
			var cnt int64
			_ = catRows.Scan(&name, &icon, &t, &cnt)
			label := "Uncategorized"
			if name != nil {
				label = *name
			}
			ic := "tag"
			if icon != nil {
				ic = *icon
			}
			byCategory = append(byCategory, map[string]any{"category": label, "icon": ic, "total": t, "count": cnt})
		}
	}
	if byCategory == nil {
		byCategory = []map[string]any{}
	}

	// By member (paid)
	memRows, _ := h.Pool.Query(r.Context(), `
		SELECT u.id, u.name, coalesce(sum(ROUND(e.amount * COALESCE(e.exchange_rate,1))::bigint),0) AS total, count(e.id)
		FROM users u
		LEFT JOIN expenses e ON e.paid_by=u.id AND e.group_id=$1 AND e.deleted_at IS NULL
		WHERE u.id IN (SELECT user_id FROM group_members WHERE group_id=$1)
		GROUP BY u.id, u.name`, groupID)
	var byMember []map[string]any
	if memRows != nil {
		defer memRows.Close()
		for memRows.Next() {
			var uid2 uuid.UUID
			var name string
			var t int64
			var cnt int64
			_ = memRows.Scan(&uid2, &name, &t, &cnt)
			byMember = append(byMember, map[string]any{"user_id": uid2, "name": name, "total_paid": t, "count": cnt})
		}
	}
	if byMember == nil {
		byMember = []map[string]any{}
	}

	// Monthly (with range)
	monthQuery := `SELECT to_char(expense_date, 'YYYY-MM') AS month, sum(ROUND(amount * COALESCE(exchange_rate,1))::bigint) FROM expenses
		WHERE group_id=$1 AND deleted_at IS NULL` + dateFilter + ` GROUP BY month ORDER BY month`
	monthArgs := []any{groupID}
	monthArgs = append(monthArgs, dateArgs...)
	monthRows, _ := h.Pool.Query(r.Context(), monthQuery, monthArgs...)
	var monthly []map[string]any
	if monthRows != nil {
		defer monthRows.Close()
		for monthRows.Next() {
			var m string
			var t int64
			_ = monthRows.Scan(&m, &t)
			monthly = append(monthly, map[string]any{"month": m, "total": t})
		}
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
