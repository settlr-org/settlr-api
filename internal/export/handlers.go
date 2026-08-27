package export

import (
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nabinkhanal00/settlr-api/internal/httpx"
)

type Handler struct {
	Pool *pgxpool.Pool
}

func (h *Handler) RegisterRoutes(mux *http.ServeMux, authMw func(http.Handler) http.Handler) {
	mux.Handle("GET /api/v1/groups/{id}/export.csv", authMw(http.HandlerFunc(h.GroupCSV)))
	mux.Handle("GET /api/v1/groups/{id}/export.json", authMw(http.HandlerFunc(h.GroupJSON)))
	mux.Handle("GET /api/v1/groups/{id}/export", authMw(http.HandlerFunc(h.GroupExport)))
	mux.Handle("GET /api/v1/me/export.csv", authMw(http.HandlerFunc(h.MyCSV)))
	mux.Handle("GET /api/v1/me/export.json", authMw(http.HandlerFunc(h.MyJSON)))
}

// MyCSV exports every expense across all of the user's groups.
func (h *Handler) MyCSV(w http.ResponseWriter, r *http.Request) {
	uid, _ := httpx.GetUserID(r.Context())
	userID, _ := uuid.Parse(uid)

	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", "attachment; filename=\"settlr-all-data.csv\"")
	w.Write([]byte("type,date,group,description,category,paid_by,amount,currency,split_user,split_amount,split_mode\n"))

	rows, err := h.Pool.Query(r.Context(), `
		SELECT e.expense_date, g.name, e.description, coalesce(c.name,''),
		       pu.name, e.amount, e.currency, e.split_mode,
		       su.name, coalesce(s.amount,0)
		FROM expenses e
		JOIN groups g ON g.id = e.group_id
		JOIN group_members gm ON gm.group_id = e.group_id AND gm.user_id=$1
		JOIN users pu ON pu.id = e.paid_by
		LEFT JOIN categories c ON c.id = e.category_id
		LEFT JOIN expense_splits s ON s.expense_id = e.id
		LEFT JOIN users su ON su.id = s.user_id
		WHERE e.deleted_at IS NULL
		ORDER BY e.expense_date, e.created_at`, userID)
	if err != nil {
		return
	}
	defer rows.Close()
	for rows.Next() {
		var date time.Time
		var groupName, description, category, paidBy, currency, splitMode string
		var amount int64
		var splitUser *string
		var splitAmount int64
		if err := rows.Scan(&date, &groupName, &description, &category, &paidBy, &amount, &currency, &splitMode, &splitUser, &splitAmount); err != nil {
			return
		}
		su := ""
		if splitUser != nil {
			su = *splitUser
		}
		fmt.Fprintf(w, "expense,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s\n",
			csvField(date.Format("2006-01-02")), csvField(groupName), csvField(description), csvField(category), csvField(paidBy),
			money(amount), csvField(currency), csvField(su), money(splitAmount), csvField(splitMode))
	}
}

func csvField(s string) string {
	if strings.ContainsAny(s, ",\n\"") {
		return "\"" + strings.ReplaceAll(s, "\"", "\"\"") + "\""
	}
	return s
}

func money(c int64) string {
	sign := ""
	if c < 0 {
		sign = "-"
		c = -c
	}
	return fmt.Sprintf("%s%d.%02d", sign, c/100, c%100)
}

func (h *Handler) GroupExport(w http.ResponseWriter, r *http.Request) {
	format := r.URL.Query().Get("format")
	if format == "json" {
		h.GroupJSON(w, r)
		return
	}
	h.GroupCSV(w, r)
}

func (h *Handler) MyJSON(w http.ResponseWriter, r *http.Request) {
	uid, _ := httpx.GetUserID(r.Context())
	userID, _ := uuid.Parse(uid)
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Disposition", "attachment; filename=\"settlr-all-data.json\"")
	rows, err := h.Pool.Query(r.Context(), `
		SELECT e.id, e.group_id, g.name, e.description, coalesce(c.name,''), pu.name, e.amount, e.currency, e.split_mode, e.expense_date, e.created_at
		FROM expenses e
		JOIN groups g ON g.id = e.group_id
		JOIN group_members gm ON gm.group_id = e.group_id AND gm.user_id=$1
		JOIN users pu ON pu.id = e.paid_by
		LEFT JOIN categories c ON c.id = e.category_id
		WHERE e.deleted_at IS NULL ORDER BY e.expense_date, e.created_at`, userID)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	defer rows.Close()
	var out []map[string]any
	for rows.Next() {
		var id, gid uuid.UUID
		var gname, desc, cat, paidBy, currency, splitMode string
		var amount int64
		var ed, ca time.Time
		_ = rows.Scan(&id, &gid, &gname, &desc, &cat, &paidBy, &amount, &currency, &splitMode, &ed, &ca)
		out = append(out, map[string]any{"id": id, "group_id": gid, "group_name": gname, "description": desc, "category": cat, "paid_by": paidBy, "amount": amount, "currency": currency, "split_mode": splitMode, "expense_date": ed.Format("2006-01-02"), "created_at": ca})
	}
	if out == nil {
		out = []map[string]any{}
	}
	httpx.WriteJSON(w, 200, map[string]any{"data": out})
}

func (h *Handler) GroupJSON(w http.ResponseWriter, r *http.Request) {
	groupID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		httpx.WriteJSON(w, 422, map[string]any{"error": map[string]string{"code": "VALIDATION_ERROR", "message": "invalid group id"}})
		return
	}
	uid, _ := httpx.GetUserID(r.Context())
	userID, _ := uuid.Parse(uid)
	var ok bool
	_ = h.Pool.QueryRow(r.Context(), "SELECT true FROM group_members WHERE group_id=$1 AND user_id=$2", groupID, userID).Scan(&ok)
	if !ok {
		httpx.WriteError(w, r, httpx.ErrGroupMemberRequired)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Disposition", "attachment; filename=\"settlr-"+groupID.String()+".json\"")
	rows, err := h.Pool.Query(r.Context(), `
		SELECT e.id, e.description, coalesce(c.name,''), pu.name, e.amount, e.currency, e.split_mode, e.expense_date, e.created_at, e.notes
		FROM expenses e JOIN users pu ON pu.id=e.paid_by LEFT JOIN categories c ON c.id=e.category_id
		WHERE e.group_id=$1 AND e.deleted_at IS NULL ORDER BY e.expense_date, e.created_at`, groupID)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	defer rows.Close()
	var out []map[string]any
	for rows.Next() {
		var id uuid.UUID
		var desc, cat, paidBy, currency, splitMode, notes string
		var amount int64
		var ed, ca time.Time
		_ = rows.Scan(&id, &desc, &cat, &paidBy, &amount, &currency, &splitMode, &ed, &ca, &notes)
		out = append(out, map[string]any{"id": id, "description": desc, "category": cat, "paid_by": paidBy, "amount": amount, "currency": currency, "split_mode": splitMode, "expense_date": ed.Format("2006-01-02"), "created_at": ca, "notes": notes})
	}
	if out == nil {
		out = []map[string]any{}
	}
	httpx.WriteJSON(w, 200, map[string]any{"data": out})
}

func (h *Handler) GroupCSV(w http.ResponseWriter, r *http.Request) {
	groupID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		httpx.WriteJSON(w, 422, map[string]any{"error": map[string]string{"code": "VALIDATION_ERROR", "message": "invalid group id"}})
		return
	}
	uid, _ := httpx.GetUserID(r.Context())
	userID, _ := uuid.Parse(uid)
	var ok bool
	_ = h.Pool.QueryRow(r.Context(), "SELECT true FROM group_members WHERE group_id=$1 AND user_id=$2", groupID, userID).Scan(&ok)
	if !ok {
		httpx.WriteError(w, r, httpx.ErrGroupMemberRequired)
		return
	}

	var groupName string
	_ = h.Pool.QueryRow(r.Context(), "SELECT name FROM groups WHERE id=$1", groupID).Scan(&groupName)

	filename := "settlr-" + strings.ReplaceAll(strings.ToLower(groupName), " ", "-") + ".csv"
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", "attachment; filename=\""+url.PathEscape(filename)+"\"")

	w.Write([]byte("type,date,description,category,paid_by,amount,currency,split_user,split_amount,split_mode\n"))

	rows, err := h.Pool.Query(r.Context(), `
		SELECT e.expense_date, e.description, coalesce(c.name,''),
		       pu.name, e.amount, e.currency, e.split_mode,
		       su.name, coalesce(s.amount,0)
		FROM expenses e
		JOIN users pu ON pu.id = e.paid_by
		LEFT JOIN categories c ON c.id = e.category_id
		LEFT JOIN expense_splits s ON s.expense_id = e.id
		LEFT JOIN users su ON su.id = s.user_id
		WHERE e.group_id=$1 AND e.deleted_at IS NULL
		ORDER BY e.expense_date, e.created_at`, groupID)
	if err != nil {
		return
	}
	defer rows.Close()
	for rows.Next() {
		var date time.Time
		var description, category, paidBy, currency, splitMode string
		var amount int64
		var splitUser *string
		var splitAmount int64
		if err := rows.Scan(&date, &description, &category, &paidBy, &amount, &currency, &splitMode, &splitUser, &splitAmount); err != nil {
			log.Printf("export: scan failed: %v", err)
			return
		}
		dateStr := date.Format("2006-01-02")
		su := ""
		if splitUser != nil {
			su = *splitUser
		}
		fmt.Fprintf(w, "expense,%s,%s,%s,%s,%s,%s,%s,%s,%s\n",
			csvField(dateStr), csvField(description), csvField(category), csvField(paidBy),
			money(amount), csvField(currency), csvField(su), money(splitAmount), csvField(splitMode))
	}

	settleRows, err := h.Pool.Query(r.Context(), `
		SELECT st.settled_at, f.name, t.name, st.amount, st.currency, st.note
		FROM settlements st
		JOIN users f ON f.id = st.from_user
		JOIN users t ON t.id = st.to_user
		WHERE st.group_id=$1 AND st.deleted_at IS NULL
		ORDER BY st.settled_at`, groupID)
	if err != nil {
		return
	}
	defer settleRows.Close()
	for settleRows.Next() {
		var settledAt time.Time
		var fromName, toName, currency, note string
		var amount int64
		if err := settleRows.Scan(&settledAt, &fromName, &toName, &amount, &currency, &note); err != nil {
			return
		}
		fmt.Fprintf(w, "settlement,%s,%s paid %s,,,,%s,%s,,%s\n",
			csvField(settledAt.Format("2006-01-02")), csvField(fromName), csvField(toName), money(amount), csvField(currency), csvField(note))
	}
}
