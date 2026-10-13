package personal

import (
	"context"
	"encoding/csv"
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
	"github.com/settlr-org/settlr-api/internal/wallet"
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

// Personal expenses are EXPENSE transactions since the wallet migration (0013). The
// legacy base_currency/exchange_rate/base_amount fields are accepted but no longer
// stored (no client used them) and are always returned as null.
func personalJSON(id uuid.UUID, description string, amount int64, currency string, categoryID uuid.UUID, notes string, date pgtype.Date, createdAt pgtype.Timestamptz) map[string]any {
	return map[string]any{
		"id":            id,
		"description":   description,
		"amount":        amount,
		"currency":      strings.TrimSpace(currency),
		"category_id":   uuidToPtr(categoryID),
		"notes":         notes,
		"expense_date":  dateToString(date),
		"base_currency": nil,
		"exchange_rate": nil,
		"base_amount":   nil,
		"created_at":    createdAt.Time,
	}
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
		UserID: userID,
		Q:      q,
		Lim:    int32(limit),
	})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	out := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		out = append(out, personalJSON(row.ID, row.Description, row.Amount, row.Currency, row.CategoryID, row.Notes, row.ExpenseDate, row.CreatedAt))
	}
	httpx.WriteJSON(w, 200, map[string]any{"data": out})
}

func (h *Handler) categoryVisible(ctx context.Context, userID, catID uuid.UUID) bool {
	ok, err := h.Queries.CategoryVisible(ctx, db.CategoryVisibleParams{
		ID:     pgtype.UUID{Bytes: catID, Valid: true},
		UserID: pgtype.UUID{Bytes: userID, Valid: true},
	})
	return err == nil && ok
}

func (h *Handler) Create(w http.ResponseWriter, r *http.Request) {
	h.ensureQueries()
	ctx := r.Context()
	uid, _ := httpx.GetUserID(ctx)
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
	cur := ""
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
			if !h.categoryVisible(ctx, userID, id) {
				httpx.WriteJSON(w, 422, map[string]any{"error": map[string]string{"code": "VALIDATION_ERROR", "message": "category_id not found"}})
				return
			}
			catID = &id
		}
	}
	occurred := time.Now().UTC()
	if req.ExpenseDate != nil && *req.ExpenseDate != "" {
		if d, err := time.Parse("2006-01-02", *req.ExpenseDate); err == nil {
			occurred = d
		}
	}
	notes := ""
	if req.Notes != nil {
		notes = *req.Notes
	}
	var catUUID pgtype.UUID
	if catID != nil {
		catUUID = pgtype.UUID{Bytes: *catID, Valid: true}
	}
	tx, err := h.Pool.Begin(ctx)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := h.Queries.WithTx(tx)
	acct, err := wallet.AccountForCurrency(ctx, qtx, userID, cur)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	cur = strings.TrimSpace(acct.Currency)
	t, err := qtx.CreateTransaction(ctx, db.CreateTransactionParams{
		ID:          uuid.New(),
		UserID:      userID,
		Type:        "EXPENSE",
		AccountID:   pgtype.UUID{Bytes: acct.ID, Valid: true},
		Amount:      req.Amount,
		Currency:    cur,
		CategoryID:  catUUID,
		Title:       req.Description,
		Description: notes,
		OccurredAt:  pgtype.Timestamptz{Time: occurred, Valid: true},
		Source:      "PERSONAL_LEGACY",
	})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if err := tx.Commit(ctx); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, 201, map[string]any{"id": t.ID, "description": req.Description, "amount": req.Amount, "currency": cur, "category_id": catID, "notes": notes, "expense_date": occurred.Format("2006-01-02")})
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
	httpx.WriteJSON(w, 200, personalJSON(row.ID, row.Description, row.Amount, row.Currency, row.CategoryID, row.Notes, row.ExpenseDate, row.CreatedAt))
}

// Update keeps the legacy PATCH semantics: each present, valid field is applied and
// invalid values are ignored. A currency change moves the expense to the account
// for that currency.
func (h *Handler) Update(w http.ResponseWriter, r *http.Request) {
	h.ensureQueries()
	ctx := r.Context()
	uid, _ := httpx.GetUserID(ctx)
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
	if _, err := h.Queries.GetPersonalExpense(ctx, db.GetPersonalExpenseParams{ID: id, UserID: userID}); err != nil {
		httpx.WriteError(w, r, httpx.ErrNotFound)
		return
	}
	t, err := h.Queries.GetTransaction(ctx, db.GetTransactionParams{ID: id, UserID: userID})
	if err != nil {
		httpx.WriteError(w, r, httpx.ErrNotFound)
		return
	}
	p := db.UpdateTransactionParams{
		ID: id, UserID: userID, Type: t.Type, AccountID: t.AccountID, Amount: t.Amount, Currency: strings.TrimSpace(t.Currency),
		Title: t.Title, Description: t.Description, OccurredAt: t.OccurredAt, DueAt: t.DueAt,
	}
	if t.CategoryID != uuid.Nil {
		p.CategoryID = pgtype.UUID{Bytes: t.CategoryID, Valid: true}
	}
	if d := strings.TrimSpace(req.Description); d != "" {
		p.Title = d
	}
	if req.Amount > 0 {
		p.Amount = req.Amount
	}
	if req.CategoryID != nil {
		if *req.CategoryID == "" {
			p.CategoryID = pgtype.UUID{}
		} else if cid, err := uuid.Parse(*req.CategoryID); err == nil && h.categoryVisible(ctx, userID, cid) {
			p.CategoryID = pgtype.UUID{Bytes: cid, Valid: true}
		}
	}
	if req.Notes != nil {
		p.Description = *req.Notes
	}
	if req.ExpenseDate != nil && *req.ExpenseDate != "" {
		if d, err := time.Parse("2006-01-02", *req.ExpenseDate); err == nil {
			p.OccurredAt = pgtype.Timestamptz{Time: d, Valid: true}
		}
	}
	tx, err := h.Pool.Begin(ctx)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := h.Queries.WithTx(tx)
	if req.Currency != nil {
		c := strings.ToUpper(strings.TrimSpace(*req.Currency))
		if auth.SupportedCurrencies[c] && c != p.Currency {
			acct, err := wallet.AccountForCurrency(ctx, qtx, userID, c)
			if err != nil {
				httpx.WriteError(w, r, err)
				return
			}
			p.AccountID = pgtype.UUID{Bytes: acct.ID, Valid: true}
			p.Currency = strings.TrimSpace(acct.Currency)
		}
	}
	if _, err := qtx.UpdateTransaction(ctx, p); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if err := tx.Commit(ctx); err != nil {
		httpx.WriteError(w, r, err)
		return
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
