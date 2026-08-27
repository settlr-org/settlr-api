package personal

import (
	"encoding/csv"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nabinkhanal00/settlr-api/internal/auth"
	"github.com/nabinkhanal00/settlr-api/internal/httpx"
)

type Handler struct{ Pool *pgxpool.Pool }

func (h *Handler) RegisterRoutes(mux *http.ServeMux, authMw func(http.Handler) http.Handler) {
	mux.Handle("GET /api/v1/personal/expenses", authMw(http.HandlerFunc(h.List)))
	mux.Handle("POST /api/v1/personal/expenses", authMw(http.HandlerFunc(h.Create)))
	mux.Handle("GET /api/v1/personal/expenses/{id}", authMw(http.HandlerFunc(h.Get)))
	mux.Handle("PATCH /api/v1/personal/expenses/{id}", authMw(http.HandlerFunc(h.Update)))
	mux.Handle("DELETE /api/v1/personal/expenses/{id}", authMw(http.HandlerFunc(h.Delete)))
	mux.Handle("GET /api/v1/personal/stats", authMw(http.HandlerFunc(h.Stats)))
	mux.Handle("GET /api/v1/personal/export.csv", authMw(http.HandlerFunc(h.ExportCSV)))
	mux.Handle("GET /api/v1/personal/budget", authMw(http.HandlerFunc(h.GetBudget)))
	mux.Handle("PUT /api/v1/personal/budget", authMw(http.HandlerFunc(h.PutBudget)))
}

func budgetMonth(r *http.Request) time.Time {
	month := strings.TrimSpace(r.URL.Query().Get("month"))
	if month == "" {
		now := time.Now().UTC()
		return time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	}
	t, err := time.Parse("2006-01", month)
	if err != nil {
		return time.Time{}
	}
	return t
}

func (h *Handler) GetBudget(w http.ResponseWriter, r *http.Request) {
	uid, _ := httpx.GetUserID(r.Context())
	userID, _ := uuid.Parse(uid)
	month := budgetMonth(r)
	if month.IsZero() {
		httpx.WriteJSON(w, 422, map[string]any{"error": map[string]string{"code": "VALIDATION_ERROR", "message": "month must be YYYY-MM"}})
		return
	}
	var amount int64
	var currency string
	err := h.Pool.QueryRow(r.Context(), `SELECT amount, currency FROM personal_budgets WHERE user_id=$1 AND month=$2`, userID, month).Scan(&amount, &currency)
	if err != nil {
		amount = 250000
		currency = "NPR"
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"month": month.Format("2006-01"), "amount": amount, "currency": strings.TrimSpace(currency)})
}

func (h *Handler) PutBudget(w http.ResponseWriter, r *http.Request) {
	uid, _ := httpx.GetUserID(r.Context())
	userID, _ := uuid.Parse(uid)
	month := budgetMonth(r)
	if month.IsZero() {
		httpx.WriteJSON(w, 422, map[string]any{"error": map[string]string{"code": "VALIDATION_ERROR", "message": "month must be YYYY-MM"}})
		return
	}
	var req struct {
		Amount   int64  `json:"amount"`
		Currency string `json:"currency"`
	}
	if err := httpx.DecodeJSON(r, &req); err != nil || req.Amount < 0 {
		httpx.WriteJSON(w, 422, map[string]any{"error": map[string]string{"code": "VALIDATION_ERROR", "message": "amount must be zero or greater"}})
		return
	}
	if req.Currency == "" {
		req.Currency = "NPR"
	}
	req.Currency = strings.ToUpper(strings.TrimSpace(req.Currency))
	if !auth.SupportedCurrencies[req.Currency] {
		httpx.WriteJSON(w, 422, map[string]any{"error": map[string]string{"code": "VALIDATION_ERROR", "message": "unsupported currency"}})
		return
	}
	_, err := h.Pool.Exec(r.Context(), `INSERT INTO personal_budgets(user_id,month,amount,currency) VALUES($1,$2,$3,$4) ON CONFLICT(user_id,month) DO UPDATE SET amount=EXCLUDED.amount,currency=EXCLUDED.currency,updated_at=now()`, userID, month, req.Amount, req.Currency)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"month": month.Format("2006-01"), "amount": req.Amount, "currency": req.Currency})
}

type createReq struct {
	Description  string   `json:"description"`
	Amount       int64    `json:"amount"`
	Currency     *string  `json:"currency"`
	CategoryID   *string  `json:"category_id"`
	Notes        *string  `json:"notes"`
	ExpenseDate  *string  `json:"expense_date"`
	ExchangeRate *float64 `json:"exchange_rate"`
	BaseCurrency *string  `json:"base_currency"`
	BaseAmount   *int64   `json:"base_amount"`
}

func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	uid, _ := httpx.GetUserID(r.Context())
	userID, _ := uuid.Parse(uid)
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	limitStr := r.URL.Query().Get("limit")
	limit := 50
	if v, err := strconv.Atoi(limitStr); err == nil && v > 0 && v <= 100 {
		limit = v
	}
	rows, err := h.Pool.Query(r.Context(), `
		SELECT id, description, amount, currency, category_id, notes, expense_date, base_currency, exchange_rate, base_amount, created_at
		FROM personal_expenses WHERE user_id=$1 AND deleted_at IS NULL AND ($2='' OR description ILIKE '%' || $2 || '%' OR notes ILIKE '%' || $2 || '%')
		ORDER BY expense_date DESC, created_at DESC LIMIT $3`, userID, q, limit)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	defer rows.Close()
	var out []map[string]any
	for rows.Next() {
		var id uuid.UUID
		var desc, cur, notes string
		var amt int64
		var catID *uuid.UUID
		var expDate time.Time
		var baseCur *string
		var rate *float64
		var baseAmt *int64
		var created time.Time
		_ = rows.Scan(&id, &desc, &amt, &cur, &catID, &notes, &expDate, &baseCur, &rate, &baseAmt, &created)
		out = append(out, map[string]any{"id": id, "description": desc, "amount": amt, "currency": cur, "category_id": catID, "notes": notes, "expense_date": expDate.Format("2006-01-02"), "base_currency": baseCur, "exchange_rate": rate, "base_amount": baseAmt, "created_at": created})
	}
	if out == nil {
		out = []map[string]any{}
	}
	httpx.WriteJSON(w, 200, map[string]any{"data": out})
}

func (h *Handler) Create(w http.ResponseWriter, r *http.Request) {
	uid, _ := httpx.GetUserID(r.Context())
	userID, _ := uuid.Parse(uid)
	var req createReq
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteJSON(w, 400, map[string]any{"error": map[string]string{"code": "VALIDATION_ERROR", "message": err.Error()}})
		return
	}
	req.Description = strings.TrimSpace(req.Description)
	if req.Description == "" || len(req.Description) > 200 {
		httpx.WriteJSON(w, 422, map[string]any{"error": map[string]string{"code": "VALIDATION_ERROR", "message": "description required (max 200)"}})
		return
	}
	if req.Amount <= 0 {
		httpx.WriteJSON(w, 422, map[string]any{"error": map[string]string{"code": "VALIDATION_ERROR", "message": "amount must be >0"}})
		return
	}
	cur := "NPR"
	if req.Currency != nil {
		cur = strings.ToUpper(strings.TrimSpace(*req.Currency))
		if !auth.SupportedCurrencies[cur] {
			httpx.WriteJSON(w, 422, map[string]any{"error": map[string]string{"code": "VALIDATION_ERROR", "message": "unsupported currency"}})
			return
		}
	}
	var catID *uuid.UUID
	if req.CategoryID != nil && *req.CategoryID != "" {
		if id, err := uuid.Parse(*req.CategoryID); err == nil {
			catID = &id
		}
	}
	date := time.Now()
	if req.ExpenseDate != nil && *req.ExpenseDate != "" {
		if d, err := time.Parse("2006-01-02", *req.ExpenseDate); err == nil {
			date = d
		}
	}
	notes := ""
	if req.Notes != nil {
		notes = *req.Notes
	}
	id := uuid.New()
	_, err := h.Pool.Exec(r.Context(), `INSERT INTO personal_expenses (id, user_id, description, amount, currency, category_id, notes, expense_date, base_currency, exchange_rate, base_amount)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`, id, userID, req.Description, req.Amount, cur, catID, notes, date, req.BaseCurrency, req.ExchangeRate, req.BaseAmount)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, 201, map[string]any{"id": id, "description": req.Description, "amount": req.Amount, "currency": cur, "category_id": catID, "notes": notes, "expense_date": date.Format("2006-01-02")})
}

func (h *Handler) Get(w http.ResponseWriter, r *http.Request) {
	uid, _ := httpx.GetUserID(r.Context())
	userID, _ := uuid.Parse(uid)
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		httpx.WriteJSON(w, 422, map[string]any{"error": map[string]string{"code": "VALIDATION_ERROR", "message": "invalid id"}})
		return
	}
	var desc, cur, notes string
	var amt int64
	var catID *uuid.UUID
	var expDate time.Time
	var created time.Time
	var baseCur *string
	var rate *float64
	var baseAmt *int64
	err = h.Pool.QueryRow(r.Context(), `SELECT description, amount, currency, category_id, notes, expense_date, created_at, base_currency, exchange_rate, base_amount FROM personal_expenses WHERE id=$1 AND user_id=$2 AND deleted_at IS NULL`, id, userID).Scan(&desc, &amt, &cur, &catID, &notes, &expDate, &created, &baseCur, &rate, &baseAmt)
	if err != nil {
		httpx.WriteError(w, r, httpx.ErrNotFound)
		return
	}
	httpx.WriteJSON(w, 200, map[string]any{"id": id, "description": desc, "amount": amt, "currency": cur, "category_id": catID, "notes": notes, "expense_date": expDate.Format("2006-01-02"), "created_at": created, "base_currency": baseCur, "exchange_rate": rate, "base_amount": baseAmt})
}

func (h *Handler) Update(w http.ResponseWriter, r *http.Request) {
	uid, _ := httpx.GetUserID(r.Context())
	userID, _ := uuid.Parse(uid)
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		httpx.WriteJSON(w, 422, map[string]any{"error": map[string]string{"code": "VALIDATION_ERROR", "message": "invalid id"}})
		return
	}
	var req createReq
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteJSON(w, 400, map[string]any{"error": map[string]string{"code": "VALIDATION_ERROR", "message": err.Error()}})
		return
	}
	// fetch existing
	var exists bool
	_ = h.Pool.QueryRow(r.Context(), `SELECT true FROM personal_expenses WHERE id=$1 AND user_id=$2 AND deleted_at IS NULL`, id, userID).Scan(&exists)
	if !exists {
		httpx.WriteError(w, r, httpx.ErrNotFound)
		return
	}
	if req.Description != "" {
		d := strings.TrimSpace(req.Description)
		if d != "" {
			_, _ = h.Pool.Exec(r.Context(), `UPDATE personal_expenses SET description=$1, updated_at=now() WHERE id=$2`, d, id)
		}
	}
	if req.Amount > 0 {
		_, _ = h.Pool.Exec(r.Context(), `UPDATE personal_expenses SET amount=$1, updated_at=now() WHERE id=$2`, req.Amount, id)
	}
	if req.Currency != nil {
		c := strings.ToUpper(strings.TrimSpace(*req.Currency))
		if auth.SupportedCurrencies[c] {
			_, _ = h.Pool.Exec(r.Context(), `UPDATE personal_expenses SET currency=$1, updated_at=now() WHERE id=$2`, c, id)
		}
	}
	if req.CategoryID != nil {
		if *req.CategoryID == "" {
			_, _ = h.Pool.Exec(r.Context(), `UPDATE personal_expenses SET category_id=NULL, updated_at=now() WHERE id=$2`, id)
		} else if cid, err := uuid.Parse(*req.CategoryID); err == nil {
			_, _ = h.Pool.Exec(r.Context(), `UPDATE personal_expenses SET category_id=$1, updated_at=now() WHERE id=$2`, cid, id)
		}
	}
	if req.Notes != nil {
		_, _ = h.Pool.Exec(r.Context(), `UPDATE personal_expenses SET notes=$1, updated_at=now() WHERE id=$2`, *req.Notes, id)
	}
	if req.ExpenseDate != nil && *req.ExpenseDate != "" {
		if d, err := time.Parse("2006-01-02", *req.ExpenseDate); err == nil {
			_, _ = h.Pool.Exec(r.Context(), `UPDATE personal_expenses SET expense_date=$1, updated_at=now() WHERE id=$2`, d, id)
		}
	}
	h.Get(w, r)
}

func (h *Handler) Delete(w http.ResponseWriter, r *http.Request) {
	uid, _ := httpx.GetUserID(r.Context())
	userID, _ := uuid.Parse(uid)
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		httpx.WriteJSON(w, 422, map[string]any{"error": map[string]string{"code": "VALIDATION_ERROR", "message": "invalid id"}})
		return
	}
	_, _ = h.Pool.Exec(r.Context(), `UPDATE personal_expenses SET deleted_at=now(), updated_at=now() WHERE id=$1 AND user_id=$2`, id, userID)
	httpx.WriteJSON(w, 200, map[string]any{"message": "deleted"})
}

func (h *Handler) Stats(w http.ResponseWriter, r *http.Request) {
	uid, _ := httpx.GetUserID(r.Context())
	userID, _ := uuid.Parse(uid)
	// totals by month (last 6), by category, total
	var total int64
	_ = h.Pool.QueryRow(r.Context(), `SELECT COALESCE(SUM(amount),0) FROM personal_expenses WHERE user_id=$1 AND deleted_at IS NULL`, userID).Scan(&total)
	rows, _ := h.Pool.Query(r.Context(), `SELECT COALESCE(c.name,'Uncategorized'), SUM(pe.amount) FROM personal_expenses pe LEFT JOIN categories c ON c.id=pe.category_id WHERE pe.user_id=$1 AND pe.deleted_at IS NULL GROUP BY c.name ORDER BY SUM(pe.amount) DESC`, userID)
	var byCat []map[string]any
	if rows != nil {
		defer rows.Close()
		for rows.Next() {
			var n string
			var s int64
			_ = rows.Scan(&n, &s)
			byCat = append(byCat, map[string]any{"category": n, "total": s})
		}
	}
	if byCat == nil {
		byCat = []map[string]any{}
	}
	rows2, _ := h.Pool.Query(r.Context(), `SELECT to_char(expense_date,'YYYY-MM') as m, SUM(amount) FROM personal_expenses WHERE user_id=$1 AND deleted_at IS NULL AND expense_date >= CURRENT_DATE - INTERVAL '6 months' GROUP BY m ORDER BY m`, userID)
	var byMonth []map[string]any
	if rows2 != nil {
		defer rows2.Close()
		for rows2.Next() {
			var m string
			var s int64
			_ = rows2.Scan(&m, &s)
			byMonth = append(byMonth, map[string]any{"month": m, "total": s})
		}
	}
	if byMonth == nil {
		byMonth = []map[string]any{}
	}
	httpx.WriteJSON(w, 200, map[string]any{"total": total, "by_category": byCat, "by_month": byMonth})
}

func (h *Handler) ExportCSV(w http.ResponseWriter, r *http.Request) {
	uid, _ := httpx.GetUserID(r.Context())
	userID, _ := uuid.Parse(uid)
	rows, err := h.Pool.Query(r.Context(), `SELECT description, amount, currency, expense_date, notes FROM personal_expenses WHERE user_id=$1 AND deleted_at IS NULL ORDER BY expense_date DESC`, userID)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	defer rows.Close()
	w.Header().Set("Content-Type", "text/csv")
	w.Header().Set("Content-Disposition", "attachment; filename=personal-expenses.csv")
	cw := csv.NewWriter(w)
	_ = cw.Write([]string{"description", "amount_cents", "currency", "date", "notes"})
	for rows.Next() {
		var d, cur, notes string
		var amt int64
		var dt time.Time
		_ = rows.Scan(&d, &amt, &cur, &dt, &notes)
		_ = cw.Write([]string{d, strconv.FormatInt(amt, 10), cur, dt.Format("2006-01-02"), notes})
	}
	cw.Flush()
}
