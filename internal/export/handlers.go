package export

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"

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

func (h *Handler) isMember(ctx context.Context, groupID, userID uuid.UUID) bool {
	q := h.ensureQueries()
	if q == nil {
		return false
	}
	ok, err := q.CheckExportGroupMember(ctx, db.CheckExportGroupMemberParams{GroupID: groupID, UserID: userID})
	if err != nil {
		return false
	}
	return ok
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

	q := h.ensureQueries()
	if q == nil {
		return
	}
	rows, err := q.ExportMyCSV(r.Context(), userID)
	if err != nil {
		return
	}
	for _, row := range rows {
		dateStr := ""
		if row.ExpenseDate.Valid {
			dateStr = row.ExpenseDate.Time.Format("2006-01-02")
		}
		su := ""
		if row.SplitUser.Valid {
			su = row.SplitUser.String
		}
		fmt.Fprintf(w, "expense,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s\n",
			csvField(dateStr), csvField(row.GroupName), csvField(row.Description), csvField(row.Category), csvField(row.PaidBy),
			money(row.Amount), csvField(row.Currency), csvField(su), money(row.SplitAmount), csvField(row.SplitMode))
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
	q := h.ensureQueries()
	if q == nil {
		httpx.WriteError(w, r, fmt.Errorf("no db queries available"))
		return
	}
	rows, err := q.ExportMyJSON(r.Context(), userID)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	var out []map[string]any
	for _, row := range rows {
		ed := ""
		if row.ExpenseDate.Valid {
			ed = row.ExpenseDate.Time.Format("2006-01-02")
		}
		var ca any
		if row.CreatedAt.Valid {
			ca = row.CreatedAt.Time
		}
		out = append(out, map[string]any{"id": row.ID, "group_id": row.GroupID, "group_name": row.GroupName, "description": row.Description, "category": row.Category, "paid_by": row.PaidBy, "amount": row.Amount, "currency": row.Currency, "split_mode": row.SplitMode, "expense_date": ed, "created_at": ca})
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
	if !h.isMember(r.Context(), groupID, userID) {
		httpx.WriteError(w, r, httpx.ErrGroupMemberRequired)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Disposition", "attachment; filename=\"settlr-"+groupID.String()+".json\"")
	q := h.ensureQueries()
	if q == nil {
		httpx.WriteError(w, r, fmt.Errorf("no db queries available"))
		return
	}
	rows, err := q.ExportGroupJSON(r.Context(), groupID)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	var out []map[string]any
	for _, row := range rows {
		ed := ""
		if row.ExpenseDate.Valid {
			ed = row.ExpenseDate.Time.Format("2006-01-02")
		}
		var ca any
		if row.CreatedAt.Valid {
			ca = row.CreatedAt.Time
		}
		out = append(out, map[string]any{"id": row.ID, "description": row.Description, "category": row.Category, "paid_by": row.PaidBy, "amount": row.Amount, "currency": row.Currency, "split_mode": row.SplitMode, "expense_date": ed, "created_at": ca, "notes": row.Notes})
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
	if !h.isMember(r.Context(), groupID, userID) {
		httpx.WriteError(w, r, httpx.ErrGroupMemberRequired)
		return
	}

	q := h.ensureQueries()
	if q == nil {
		httpx.WriteError(w, r, fmt.Errorf("no db queries available"))
		return
	}
	groupName := ""
	if name, err := q.GetExportGroupName(r.Context(), groupID); err == nil {
		groupName = name
	}

	filename := "settlr-" + strings.ReplaceAll(strings.ToLower(groupName), " ", "-") + ".csv"
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", "attachment; filename=\""+url.PathEscape(filename)+"\"")

	w.Write([]byte("type,date,description,category,paid_by,amount,currency,split_user,split_amount,split_mode\n"))

	csvRows, err := q.ExportGroupCSV(r.Context(), groupID)
	if err != nil {
		return
	}
	for _, row := range csvRows {
		dateStr := ""
		if row.ExpenseDate.Valid {
			dateStr = row.ExpenseDate.Time.Format("2006-01-02")
		}
		su := ""
		if row.SplitUser.Valid {
			su = row.SplitUser.String
		}
		fmt.Fprintf(w, "expense,%s,%s,%s,%s,%s,%s,%s,%s,%s\n",
			csvField(dateStr), csvField(row.Description), csvField(row.Category), csvField(row.PaidBy),
			money(row.Amount), csvField(row.Currency), csvField(su), money(row.SplitAmount), csvField(row.SplitMode))
	}

	settleRows, err := q.ExportGroupSettlements(r.Context(), groupID)
	if err != nil {
		return
	}
	for _, row := range settleRows {
		settledAtStr := ""
		if row.SettledAt.Valid {
			settledAtStr = row.SettledAt.Time.Format("2006-01-02")
		}
		fmt.Fprintf(w, "settlement,%s,%s paid %s,,,,%s,%s,,%s\n",
			csvField(settledAtStr), csvField(row.FromName), csvField(row.ToName), money(row.Amount), csvField(row.Currency), csvField(row.Note))
	}
}
