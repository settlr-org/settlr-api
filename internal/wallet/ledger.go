package wallet

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	db "github.com/settlr-org/settlr-api/internal/db"
	"github.com/settlr-org/settlr-api/internal/httpx"
)

func ledgerJSON(e db.LedgerEntry, tags []tagRef) map[string]any {
	if tags == nil {
		tags = []tagRef{}
	}
	var toCurrency *string
	if e.ToCurrency.Valid {
		c := strings.TrimSpace(e.ToCurrency.String)
		toCurrency = &c
	}
	var groupName *string
	if e.GroupName.Valid {
		groupName = &e.GroupName.String
	}
	return map[string]any{
		"id":                 e.ID,
		"kind":               e.Kind,
		"type":               e.Type,
		"account_id":         uuidPtr(e.AccountID),
		"to_account_id":      uuidPtr(e.ToAccountID),
		"amount":             e.Amount,
		"currency":           strings.TrimSpace(e.Currency),
		"to_amount":          int8Ptr(e.ToAmount),
		"to_currency":        toCurrency,
		"account_amount":     int8Ptr(e.AccountAmount),
		"to_account_amount":  int8Ptr(e.ToAccountAmount),
		"category_id":        nilIfZero(e.CategoryID),
		"title":              e.Title,
		"description":        e.Description,
		"occurred_at":        tsPtr(e.OccurredAt),
		"due_at":             tsPtr(e.DueAt),
		"source":             e.Source,
		"group_id":           nilIfZero(e.GroupID),
		"group_name":         groupName,
		"expense_id":         nilIfZero(e.ExpenseID),
		"settlement_id":      uuidPtr(e.SettlementID),
		"total_amount":       int8Ptr(e.TotalAmount),
		"planned_payment_id": uuidPtr(e.PlannedPaymentID),
		"loan_id":            uuidPtr(e.LoanID),
		"loan_record_id":     uuidPtr(e.LoanRecordID),
		// Group entries are edited in the group, loan bookings through the loan.
		"read_only":  e.Kind != "TRANSACTION" || e.Source == "LOAN",
		"tags":       tags,
		"created_at": e.CreatedAt.Time.UTC(),
	}
}

// ledgerPage runs a ledger query (p.Lim should be limit+1 to detect a next page)
// and renders the entries with their tags.
func (h *Handler) ledgerPage(ctx context.Context, userID uuid.UUID, p db.ListLedgerEntriesParams, limit int) ([]map[string]any, *string, error) {
	for _, s := range []*[]string{&p.Kinds, &p.Types, &p.Sources, &p.ExclSources, &p.QTerms, &p.ExclQTerms} {
		if *s == nil {
			*s = []string{}
		}
	}
	for _, s := range []*[]pgtype.UUID{&p.AccountIds, &p.CategoryIds, &p.GroupIds, &p.TagIds, &p.ExclTagIds} {
		if *s == nil {
			*s = []pgtype.UUID{}
		}
	}
	if p.Planned == "" {
		p.Planned = "exclude"
	}
	rows, err := h.q().ListLedgerEntries(ctx, p)
	if err != nil {
		return nil, nil, err
	}
	var next *string
	if len(rows) > limit {
		rows = rows[:limit]
		last := rows[len(rows)-1]
		c := encodeCursor(last.SortAt.Time, last.ID)
		next = &c
	}
	var txIDs []uuid.UUID
	for _, e := range rows {
		if e.Kind == "TRANSACTION" {
			txIDs = append(txIDs, e.ID)
		}
	}
	tags, err := h.tagsFor(ctx, txIDs)
	if err != nil {
		return nil, nil, err
	}
	out := make([]map[string]any, 0, len(rows))
	for _, e := range rows {
		var t []tagRef
		if e.Kind == "TRANSACTION" {
			t = tags[e.ID]
		}
		out = append(out, ledgerJSON(e, t))
	}
	return out, next, nil
}

// Ledger lists wallet transactions together with group expense shares and
// settlements booked to accounts (read-only), newest first.
func (h *Handler) Ledger(w http.ResponseWriter, r *http.Request) {
	userID := currentUser(r)
	f, msg := parseFilters(r, true)
	if msg != "" {
		validation(w, msg)
		return
	}
	limit := queryLimit(r, 50, 200)
	out, next, err := h.ledgerPage(r.Context(), userID, db.ListLedgerEntriesParams{
		UserID: userID, Planned: f.planned, FromAt: f.from, ToAt: f.to, Kinds: f.kinds, Types: f.types, Sources: f.sources, ExclSources: f.exclSources,
		AccountIds: f.accounts, CategoryIds: f.categories, GroupIds: f.groups, TagIds: f.tags, ExclTagIds: f.exclTags,
		QTerms: f.qTerms, ExclQTerms: f.exclQTerms, CategoryNone: f.catNone, MinAmount: f.min, MaxAmount: f.max, CursorAt: f.cursorAt, CursorID: f.cursorID,
		Lim: int32(limit + 1),
	}, limit)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	resp := map[string]any{"data": out}
	if next != nil {
		resp["next_cursor"] = *next
	}
	httpx.WriteJSON(w, http.StatusOK, resp)
}

// currentPeriod returns the user's current "month" given month_start_day (UTC).
func currentPeriod(now time.Time, startDay int) (time.Time, time.Time) {
	now = now.UTC()
	start := time.Date(now.Year(), now.Month(), startDay, 0, 0, 0, 0, time.UTC)
	if now.Before(start) {
		start = start.AddDate(0, -1, 0)
	}
	return start, start.AddDate(0, 1, 0)
}

// flowTotals sums income/expense in the base currency.
func (h *Handler) flowTotals(ctx context.Context, userID uuid.UUID, f *fx, from, to *time.Time, category *uuid.UUID) (income, expense int64, count int, err error) {
	rows, err := h.q().LedgerFlowTotals(ctx, db.LedgerFlowTotalsParams{
		UserID: userID, FromAt: ts(from), ToAt: ts(to), CategoryID: pgUUID(category),
	})
	if err != nil {
		return 0, 0, 0, err
	}
	for _, row := range rows {
		if v, ok := f.convert(ctx, row.Income, row.Currency, f.base); ok {
			income += v
		}
		if v, ok := f.convert(ctx, row.Expense, row.Currency, f.base); ok {
			expense += v
		}
		count += int(row.Entries)
	}
	return income, expense, count, nil
}

// Overview returns the total balance (base currency, accounts included in the
// balance), income and expense for [from, to) — default: the current period per
// month_start_day — the unpaid planned items (overdue: due before now; upcoming:
// due now or later within the range) and the projected balance at the end of the
// range (balance plus every unpaid planned item due before `to`).
func (h *Handler) Overview(w http.ResponseWriter, r *http.Request) {
	userID := currentUser(r)
	ctx := r.Context()
	from, to, err := parseRange(r)
	if err != nil {
		validation(w, err.Error())
		return
	}
	if from == nil && to == nil {
		s, err := h.loadSettings(ctx, userID)
		if err != nil {
			httpx.WriteError(w, r, err)
			return
		}
		a, b := currentPeriod(time.Now(), int(s.MonthStartDay))
		from, to = &a, &b
	}
	accounts, err := h.q().ListAccounts(ctx, userID)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	f, err := h.newFX(ctx, userID)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	bals, _, err := h.balances(ctx, userID, accounts, f)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	var total int64
	for _, a := range accounts {
		if !a.IncludeInBalance {
			continue
		}
		if v, ok := f.convert(ctx, bals[a.ID], a.Currency, f.base); ok {
			total += v
		}
	}
	income, expense, _, err := h.flowTotals(ctx, userID, f, from, to, nil)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	pv, err := h.plannedItems(ctx, userID, f, from, to, nil, nil, accounts)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"currency": f.base, "from": from, "to": to,
		"balance": total, "income": income, "expense": expense,
		"upcoming": pv.upcoming, "overdue": pv.overdue,
		"upcoming_income": pv.upIncome, "upcoming_expense": pv.upExpense,
		"overdue_income": pv.overdueIncome, "overdue_expense": pv.overdueExpense,
		"projected_balance": total + pv.projectedDelta,
		"missing_rates":     f.missingList(),
	})
}

// CategorySummary reports income/expense (base currency) for one visible category
// in [from, to), group shares included, plus the matching ledger entries.
func (h *Handler) CategorySummary(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	userID := currentUser(r)
	ctx := r.Context()
	visible, err := h.q().CategoryVisible(ctx, db.CategoryVisibleParams{ID: pu(id), UserID: pu(userID)})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if !visible {
		httpx.WriteError(w, r, httpx.ErrNotFound)
		return
	}
	from, to, err := parseRange(r)
	if err != nil {
		validation(w, err.Error())
		return
	}
	cursorAt, cursorID, err := decodeCursor(r.URL.Query().Get("cursor"))
	if err != nil {
		validation(w, err.Error())
		return
	}
	f, err := h.newFX(ctx, userID)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	income, expense, count, err := h.flowTotals(ctx, userID, f, from, to, &id)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	limit := queryLimit(r, 50, 200)
	entries, next, err := h.ledgerPage(ctx, userID, db.ListLedgerEntriesParams{
		UserID: userID, Planned: "exclude", FromAt: ts(from), ToAt: ts(to),
		CategoryIds: pgUUIDs([]uuid.UUID{id}), CursorAt: cursorAt, CursorID: cursorID, Lim: int32(limit + 1),
	}, limit)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	pv, err := h.plannedItems(ctx, userID, f, from, to, nil, &id, nil)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	resp := map[string]any{
		"category_id": id, "currency": f.base, "from": from, "to": to,
		"income": income, "expense": expense, "count": count, "entries": entries,
		"upcoming": pv.upcoming, "overdue": pv.overdue,
		"missing_rates": f.missingList(),
	}
	if next != nil {
		resp["next_cursor"] = *next
	}
	httpx.WriteJSON(w, http.StatusOK, resp)
}
