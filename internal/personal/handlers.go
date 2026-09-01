package personal

import (
	"encoding/csv"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/settlr-org/settlr-api/internal/auth"
	db "github.com/settlr-org/settlr-api/internal/db"
	"github.com/settlr-org/settlr-api/internal/httpx"
)

// Handler handles personal expense and budget requests.
// Pool is the raw pgx pool for legacy fallback; Queries is the sqlc-generated wrapper.
// New code should use Queries where possible; see internal/db/queries/personal.sql
type Handler struct {
	Pool    *pgxpool.Pool
	Queries *db.Queries
}

func (h *Handler) ensureQueries() {
	if h.Queries == nil && h.Pool != nil {
		h.Queries = db.New(h.Pool)
	}
}

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

func timeToDate(t time.Time) pgtype.Date {
	return pgtype.Date{Time: t, Valid: true}
}

func stringPtrToText(s *string) pgtype.Text {
	if s == nil {
		return pgtype.Text{Valid: false}
	}
	return pgtype.Text{String: *s, Valid: true}
}

func int64PtrToInt8(i *int64) pgtype.Int8 {
	if i == nil {
		return pgtype.Int8{Valid: false}
	}
	return pgtype.Int8{Int64: *i, Valid: true}
}

func float64PtrToNumeric(f *float64) pgtype.Numeric {
	if f == nil {
		return pgtype.Numeric{Valid: false}
	}
	var n pgtype.Numeric
	_ = n.Scan(fmt.Sprintf("%v", *f))
	return n
}

func numericToFloat64Ptr(n pgtype.Numeric) *float64 {
	if !n.Valid {
		return nil
	}
	val, err := n.Float64Value()
	if err == nil && val.Valid {
		f := val.Float64
		return &f
	}
	return nil
}

func textToStringPtr(t pgtype.Text) *string {
	if !t.Valid {
		return nil
	}
	s := t.String
	return &s
}

func int8ToInt64Ptr(i pgtype.Int8) *int64 {
	if !i.Valid {
		return nil
	}
	v := i.Int64
	return &v
}

func dateToString(d pgtype.Date) string {
	if !d.Valid {
		return ""
	}
	return d.Time.Format("2006-01-02")
}

func uuidToPtr(id uuid.UUID) *uuid.UUID {
	if id == uuid.Nil {
		return nil
	}
	tmp := id
	return &tmp
}

func (h *Handler) GetBudget(w http.ResponseWriter, r *http.Request) {
	h.ensureQueries()
	uid, _ := httpx.GetUserID(r.Context())
	userID, _ := uuid.Parse(uid)
	month := budgetMonth(r)
	if month.IsZero() {
		httpx.WriteJSON(w, 422, map[string]any{"error": map[string]string{"code": "VALIDATION_ERROR", "message": "month must be YYYY-MM"}})
		return
	}
	row, err := h.Queries.GetPersonalBudget(r.Context(), db.GetPersonalBudgetParams{
		UserID: userID,
		Month:  timeToDate(month),
	})
	var amount int64
	var currency string
	if err != nil {
		amount = 250000
		currency = "NPR"
	} else {
		amount = row.Amount
		currency = row.Currency
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"month": month.Format("2006-01"), "amount": amount, "currency": strings.TrimSpace(currency)})
}

func (h *Handler) PutBudget(w http.ResponseWriter, r *http.Request) {
	h.ensureQueries()
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
	err := h.Queries.UpsertPersonalBudget(r.Context(), db.UpsertPersonalBudgetParams{
		UserID:   userID,
		Month:    timeToDate(month),
		Amount:   req.Amount,
		Currency: req.Currency,
	})
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
	h.ensureQueries()
	uid, _ := httpx.GetUserID(r.Context())
	userID, _ := uuid.Parse(uid)
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	limitStr := r.URL.Query().Get("limit")
	limit := 50
	if v, err := strconv.Atoi(limitStr); err == nil && v > 0 && v <= 100 {
		limit = v
	}
	rows, err := h.Queries.ListPersonalExpenses(r.Context(), db.ListPersonalExpensesParams{
		UserID:  userID,
		Column2: q,
		Limit:   int32(limit),
	})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	var out []map[string]any
	for _, row := range rows {
		catID := uuidToPtr(row.CategoryID)
		out = append(out, map[string]any{
			"id":            row.ID,
			"description":   row.Description,
			"amount":        row.Amount,
			"currency":      row.Currency,
			"category_id":   catID,
			"notes":         row.Notes,
			"expense_date":  dateToString(row.ExpenseDate),
			"base_currency": textToStringPtr(row.BaseCurrency),
			"exchange_rate": numericToFloat64Ptr(row.ExchangeRate),
			"base_amount":   int8ToInt64Ptr(row.BaseAmount),
			"created_at":    row.CreatedAt.Time,
		})
	}
	if out == nil {
		out = []map[string]any{}
	}
	httpx.WriteJSON(w, 200, map[string]any{"data": out})
}

func (h *Handler) Create(w http.ResponseWriter, r *http.Request) {
	h.ensureQueries()
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
	var catUUID pgtype.UUID
	if catID != nil {
		catUUID = pgtype.UUID{Bytes: *catID, Valid: true}
	}
	err := h.Queries.CreatePersonalExpense(r.Context(), db.CreatePersonalExpenseParams{
		ID:           id,
		UserID:       userID,
		Description:  req.Description,
		Amount:       req.Amount,
		Currency:     cur,
		Column6:      catUUID,
		Notes:        notes,
		ExpenseDate:  timeToDate(date),
		BaseCurrency: stringPtrToText(req.BaseCurrency),
		ExchangeRate: float64PtrToNumeric(req.ExchangeRate),
		BaseAmount:   int64PtrToInt8(req.BaseAmount),
	})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, 201, map[string]any{"id": id, "description": req.Description, "amount": req.Amount, "currency": cur, "category_id": catID, "notes": notes, "expense_date": date.Format("2006-01-02")})
}

func (h *Handler) Get(w http.ResponseWriter, r *http.Request) {
	h.ensureQueries()
	uid, _ := httpx.GetUserID(r.Context())
	userID, _ := uuid.Parse(uid)
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		httpx.WriteJSON(w, 422, map[string]any{"error": map[string]string{"code": "VALIDATION_ERROR", "message": "invalid id"}})
		return
	}
	row, err := h.Queries.GetPersonalExpense(r.Context(), db.GetPersonalExpenseParams{
		ID:     id,
		UserID: userID,
	})
	if err != nil {
		httpx.WriteError(w, r, httpx.ErrNotFound)
		return
	}
	httpx.WriteJSON(w, 200, map[string]any{
		"id":            id,
		"description":   row.Description,
		"amount":        row.Amount,
		"currency":      row.Currency,
		"category_id":   uuidToPtr(row.CategoryID),
		"notes":         row.Notes,
		"expense_date":  dateToString(row.ExpenseDate),
		"created_at":    row.CreatedAt.Time,
		"base_currency": textToStringPtr(row.BaseCurrency),
		"exchange_rate": numericToFloat64Ptr(row.ExchangeRate),
		"base_amount":   int8ToInt64Ptr(row.BaseAmount),
	})
}

func (h *Handler) Update(w http.ResponseWriter, r *http.Request) {
	h.ensureQueries()
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
	exists, err := h.Queries.CheckPersonalExpenseExists(r.Context(), db.CheckPersonalExpenseExistsParams{
		ID:     id,
		UserID: userID,
	})
	if err != nil || !exists {
		httpx.WriteError(w, r, httpx.ErrNotFound)
		return
	}
	if req.Description != "" {
		d := strings.TrimSpace(req.Description)
		if d != "" {
			_ = h.Queries.UpdatePersonalExpenseDescription(r.Context(), db.UpdatePersonalExpenseDescriptionParams{
				Description: d,
				ID:          id,
			})
		}
	}
	if req.Amount > 0 {
		_ = h.Queries.UpdatePersonalExpenseAmount(r.Context(), db.UpdatePersonalExpenseAmountParams{
			Amount: req.Amount,
			ID:     id,
		})
	}
	if req.Currency != nil {
		c := strings.ToUpper(strings.TrimSpace(*req.Currency))
		if auth.SupportedCurrencies[c] {
			_ = h.Queries.UpdatePersonalExpenseCurrency(r.Context(), db.UpdatePersonalExpenseCurrencyParams{
				Currency: c,
				ID:       id,
			})
		}
	}
	if req.CategoryID != nil {
		if *req.CategoryID == "" {
			_ = h.Queries.UpdatePersonalExpenseCategoryClear(r.Context(), id)
		} else if cid, err := uuid.Parse(*req.CategoryID); err == nil {
			_ = h.Queries.UpdatePersonalExpenseCategory(r.Context(), db.UpdatePersonalExpenseCategoryParams{
				CategoryID: cid,
				ID:         id,
			})
		}
	}
	if req.Notes != nil {
		_ = h.Queries.UpdatePersonalExpenseNotes(r.Context(), db.UpdatePersonalExpenseNotesParams{
			Notes: *req.Notes,
			ID:    id,
		})
	}
	if req.ExpenseDate != nil && *req.ExpenseDate != "" {
		if d, err := time.Parse("2006-01-02", *req.ExpenseDate); err == nil {
			_ = h.Queries.UpdatePersonalExpenseDate(r.Context(), db.UpdatePersonalExpenseDateParams{
				ExpenseDate: timeToDate(d),
				ID:          id,
			})
		}
	}
	h.Get(w, r)
}

func (h *Handler) Delete(w http.ResponseWriter, r *http.Request) {
	h.ensureQueries()
	uid, _ := httpx.GetUserID(r.Context())
	userID, _ := uuid.Parse(uid)
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		httpx.WriteJSON(w, 422, map[string]any{"error": map[string]string{"code": "VALIDATION_ERROR", "message": "invalid id"}})
		return
	}
	_ = h.Queries.SoftDeletePersonalExpense(r.Context(), db.SoftDeletePersonalExpenseParams{
		ID:     id,
		UserID: userID,
	})
	httpx.WriteJSON(w, 200, map[string]any{"message": "deleted"})
}

func (h *Handler) Stats(w http.ResponseWriter, r *http.Request) {
	h.ensureQueries()
	uid, _ := httpx.GetUserID(r.Context())
	userID, _ := uuid.Parse(uid)
	total, _ := h.Queries.GetPersonalExpenseTotal(r.Context(), userID)
	catRows, _ := h.Queries.ListPersonalExpenseByCategory(r.Context(), userID)
	var byCat []map[string]any
	for _, row := range catRows {
		byCat = append(byCat, map[string]any{"category": row.Category, "total": row.Total})
	}
	if byCat == nil {
		byCat = []map[string]any{}
	}
	monthRows, _ := h.Queries.ListPersonalExpenseByMonth(r.Context(), userID)
	var byMonth []map[string]any
	for _, row := range monthRows {
		byMonth = append(byMonth, map[string]any{"month": row.Month, "total": row.Total})
	}
	if byMonth == nil {
		byMonth = []map[string]any{}
	}
	httpx.WriteJSON(w, 200, map[string]any{"total": total, "by_category": byCat, "by_month": byMonth})
}

func (h *Handler) ExportCSV(w http.ResponseWriter, r *http.Request) {
	h.ensureQueries()
	uid, _ := httpx.GetUserID(r.Context())
	userID, _ := uuid.Parse(uid)
	rows, err := h.Queries.ListPersonalExpensesExport(r.Context(), userID)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "text/csv")
	w.Header().Set("Content-Disposition", "attachment; filename=personal-expenses.csv")
	cw := csv.NewWriter(w)
	_ = cw.Write([]string{"description", "amount_cents", "currency", "date", "notes"})
	for _, row := range rows {
		_ = cw.Write([]string{row.Description, strconv.FormatInt(row.Amount, 10), row.Currency, dateToString(row.ExpenseDate), row.Notes})
	}
	cw.Flush()
}
