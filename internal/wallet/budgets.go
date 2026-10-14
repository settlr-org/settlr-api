package wallet

import (
	"context"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	db "github.com/settlr-org/settlr-api/internal/db"
	"github.com/settlr-org/settlr-api/internal/httpx"
)

// Budgets (Ivy budgets): a spending limit over a set of categories and accounts
// (empty = all). `spent` is computed per request for a period (default: the
// current period per month_start_day) from the unified ledger, so my shares of
// group expenses count; see BudgetSpent for the exact rule.

func uuidList(ids []pgtype.UUID) []uuid.UUID {
	out := make([]uuid.UUID, 0, len(ids))
	for _, id := range ids {
		if id.Valid {
			out = append(out, uuid.UUID(id.Bytes))
		}
	}
	return out
}

func percentOf(part, whole int64) float64 {
	if whole <= 0 {
		return 0
	}
	return math.Round(float64(part)*10000/float64(whole)) / 100
}

// budgetRange reads ?from&to (both or neither); default the current period.
func (h *Handler) budgetRange(ctx context.Context, r *http.Request, userID uuid.UUID) (time.Time, time.Time, string, error) {
	from, to, err := parseRange(r)
	if err != nil {
		return time.Time{}, time.Time{}, err.Error(), nil
	}
	if (from == nil) != (to == nil) {
		return time.Time{}, time.Time{}, "from and to must be given together", nil
	}
	if from != nil {
		if !to.After(*from) {
			return time.Time{}, time.Time{}, "to must be after from", nil
		}
		return *from, *to, "", nil
	}
	s, err := h.loadSettings(ctx, userID)
	if err != nil {
		return time.Time{}, time.Time{}, "", err
	}
	a, b := currentPeriod(time.Now(), int(s.MonthStartDay))
	return a, b, "", nil
}

type budgetTotals struct {
	amount, spent int64
}

// budgetsJSON renders budgets with spent/remaining/percent for [from, to).
func (h *Handler) budgetsJSON(ctx context.Context, userID uuid.UUID, budgets []db.Budget, from, to time.Time) ([]map[string]any, *fx, budgetTotals, error) {
	var tot budgetTotals
	f, err := h.newFX(ctx, userID)
	if err != nil {
		return nil, nil, tot, err
	}
	rows, err := h.q().BudgetSpent(ctx, db.BudgetSpentParams{UserID: userID, FromAt: ts(&from), ToAt: ts(&to)})
	if err != nil {
		return nil, nil, tot, err
	}
	byBudget := map[uuid.UUID][]db.BudgetSpentRow{}
	for _, row := range rows {
		byBudget[row.BudgetID] = append(byBudget[row.BudgetID], row)
	}
	out := make([]map[string]any, 0, len(budgets))
	for _, b := range budgets {
		cur := strings.TrimSpace(b.Currency)
		var spent int64
		missing := []string{}
		for _, row := range byBudget[b.ID] {
			if v, ok := f.convert(ctx, row.Spent, row.Currency, cur); ok {
				spent += v
			} else {
				missing = append(missing, strings.TrimSpace(row.Currency)+"/"+cur)
			}
		}
		if v, ok := f.convert(ctx, b.Amount, cur, f.base); ok {
			tot.amount += v
		}
		if v, ok := f.convert(ctx, spent, cur, f.base); ok {
			tot.spent += v
		}
		out = append(out, map[string]any{
			"id":            b.ID,
			"name":          b.Name,
			"amount":        b.Amount,
			"currency":      cur,
			"category_ids":  uuidList(b.CategoryIds),
			"account_ids":   uuidList(b.AccountIds),
			"sort_order":    b.SortOrder,
			"from":          from,
			"to":            to,
			"spent":         spent,
			"remaining":     b.Amount - spent,
			"percent":       percentOf(spent, b.Amount),
			"missing_rates": missing,
			"created_at":    b.CreatedAt.Time.UTC(),
			"updated_at":    b.UpdatedAt.Time.UTC(),
		})
	}
	return out, f, tot, nil
}

// ListBudgets returns every budget with its spending in [from, to) plus totals
// in the base currency.
func (h *Handler) ListBudgets(w http.ResponseWriter, r *http.Request) {
	userID := currentUser(r)
	ctx := r.Context()
	from, to, msg, err := h.budgetRange(ctx, r, userID)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if msg != "" {
		validation(w, msg)
		return
	}
	budgets, err := h.q().ListBudgets(ctx, userID)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	out, f, tot, err := h.budgetsJSON(ctx, userID, budgets, from, to)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"data": out, "from": from, "to": to, "currency": f.base,
		"total_amount": tot.amount, "total_spent": tot.spent, "total_remaining": tot.amount - tot.spent,
		"missing_rates": f.missingList(),
	})
}

func (h *Handler) writeBudget(w http.ResponseWriter, r *http.Request, status int, b db.Budget) {
	ctx := r.Context()
	from, to, msg, err := h.budgetRange(ctx, r, b.UserID)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if msg != "" {
		validation(w, msg)
		return
	}
	out, _, _, err := h.budgetsJSON(ctx, b.UserID, []db.Budget{b}, from, to)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, status, out[0])
}

func (h *Handler) GetBudget(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	b, err := h.q().GetBudget(r.Context(), db.GetBudgetParams{ID: id, UserID: currentUser(r)})
	if err != nil {
		httpx.WriteError(w, r, httpx.ErrNotFound)
		return
	}
	h.writeBudget(w, r, http.StatusOK, b)
}

type budgetReq struct {
	Name        Opt[string]      `json:"name"`
	Amount      Opt[int64]       `json:"amount"`
	Currency    Opt[string]      `json:"currency"`
	CategoryIDs Opt[[]uuid.UUID] `json:"category_ids"`
	AccountIDs  Opt[[]uuid.UUID] `json:"account_ids"`
}

type budgetState struct {
	name       string
	amount     int64
	currency   string
	categories []uuid.UUID
	accounts   []uuid.UUID
}

func (s *budgetState) apply(req budgetReq) {
	if v, ok := req.Name.Value(); ok {
		s.name = v
	}
	if v, ok := req.Amount.Value(); ok {
		s.amount = v
	}
	if v, ok := req.Currency.Value(); ok {
		s.currency = v
	}
	if req.CategoryIDs.Set {
		s.categories, _ = req.CategoryIDs.Value()
	}
	if req.AccountIDs.Set {
		s.accounts, _ = req.AccountIDs.Value()
	}
}

func (h *Handler) validateBudget(ctx context.Context, userID uuid.UUID, s *budgetState) (string, error) {
	s.name = strings.TrimSpace(s.name)
	if s.name == "" || len(s.name) > 100 {
		return "name required (max 100)", nil
	}
	if s.amount <= 0 || s.amount > maxAmount {
		return "amount must be > 0 (minor units)", nil
	}
	s.currency = normCurrency(s.currency)
	if s.currency == "" {
		c, err := h.q().GetUserDefaultCurrency(ctx, userID)
		if err != nil {
			return "", err
		}
		s.currency = strings.TrimSpace(c)
	}
	if !validCurrency(s.currency) {
		return "unsupported currency", nil
	}
	s.categories = uniqueUUIDs(s.categories)
	if len(s.categories) > 0 {
		n, err := h.q().CountVisibleCategories(ctx, db.CountVisibleCategoriesParams{Ids: pgUUIDs(s.categories), UserID: pu(userID)})
		if err != nil {
			return "", err
		}
		if int(n) != len(s.categories) {
			return "category_ids contains an unknown category", nil
		}
	}
	s.accounts = uniqueUUIDs(s.accounts)
	if len(s.accounts) > 0 {
		n, err := h.q().CountOwnedAccounts(ctx, db.CountOwnedAccountsParams{UserID: userID, Ids: pgUUIDs(s.accounts)})
		if err != nil {
			return "", err
		}
		if int(n) != len(s.accounts) {
			return "account_ids contains an unknown account", nil
		}
	}
	return "", nil
}

func (h *Handler) CreateBudget(w http.ResponseWriter, r *http.Request) {
	userID := currentUser(r)
	ctx := r.Context()
	var req budgetReq
	if !decode(w, r, &req) {
		return
	}
	var s budgetState
	s.apply(req)
	msg, err := h.validateBudget(ctx, userID, &s)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if msg != "" {
		validation(w, msg)
		return
	}
	next, err := h.q().NextBudgetSortOrder(ctx, userID)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	b, err := h.q().CreateBudget(ctx, db.CreateBudgetParams{
		UserID: userID, Name: s.name, Amount: s.amount, Currency: s.currency,
		CategoryIds: pgUUIDs(s.categories), AccountIds: pgUUIDs(s.accounts), SortOrder: next,
	})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	h.writeBudget(w, r, http.StatusCreated, b)
}

func (h *Handler) UpdateBudget(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	userID := currentUser(r)
	ctx := r.Context()
	cur, err := h.q().GetBudget(ctx, db.GetBudgetParams{ID: id, UserID: userID})
	if err != nil {
		httpx.WriteError(w, r, httpx.ErrNotFound)
		return
	}
	var req budgetReq
	if !decode(w, r, &req) {
		return
	}
	s := budgetState{
		name: cur.Name, amount: cur.Amount, currency: cur.Currency,
		categories: uuidList(cur.CategoryIds), accounts: uuidList(cur.AccountIds),
	}
	s.apply(req)
	msg, err := h.validateBudget(ctx, userID, &s)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if msg != "" {
		validation(w, msg)
		return
	}
	b, err := h.q().UpdateBudget(ctx, db.UpdateBudgetParams{
		ID: id, UserID: userID, Name: s.name, Amount: s.amount, Currency: s.currency,
		CategoryIds: pgUUIDs(s.categories), AccountIds: pgUUIDs(s.accounts),
	})
	if err != nil {
		httpx.WriteError(w, r, httpx.ErrNotFound)
		return
	}
	h.writeBudget(w, r, http.StatusOK, b)
}

func (h *Handler) DeleteBudget(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	n, err := h.q().DeleteBudget(r.Context(), db.DeleteBudgetParams{ID: id, UserID: currentUser(r)})
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

func (h *Handler) ReorderBudgets(w http.ResponseWriter, r *http.Request) {
	userID := currentUser(r)
	var req struct {
		IDs []uuid.UUID `json:"ids"`
	}
	if !decode(w, r, &req) {
		return
	}
	if len(req.IDs) == 0 || len(uniqueUUIDs(req.IDs)) != len(req.IDs) {
		validation(w, "ids must be a non-empty list of distinct budget ids")
		return
	}
	ctx := r.Context()
	tx, err := h.Pool.Begin(ctx)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := h.q().WithTx(tx)
	for i, id := range req.IDs {
		n, err := qtx.SetBudgetSortOrder(ctx, db.SetBudgetSortOrderParams{ID: id, UserID: userID, SortOrder: int32(i)})
		if err != nil {
			httpx.WriteError(w, r, err)
			return
		}
		if n == 0 {
			validation(w, "unknown budget id "+id.String())
			return
		}
	}
	if err := tx.Commit(ctx); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	h.ListBudgets(w, r)
}
