package search

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
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
	mux.Handle("GET /api/v1/search", authMw(http.HandlerFunc(h.Search)))
}

func (h *Handler) Search(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" {
		httpx.WriteJSON(w, 200, emptyResult())
		return
	}
	uid, _ := httpx.GetUserID(r.Context())
	userID, _ := uuid.Parse(uid)
	queries := h.ensureQueries()
	if queries == nil {
		httpx.WriteJSON(w, 200, emptyResult())
		return
	}
	uRows, _ := queries.SearchUsers(r.Context(), httpx.LikeContains(q))
	var users []map[string]any
	for _, u := range uRows {
		users = append(users, map[string]any{"id": u.ID, "name": u.Name, "avatar_url": u.AvatarUrl})
	}
	if users == nil {
		users = []map[string]any{}
	}
	gRows, _ := queries.SearchGroups(r.Context(), db.SearchGroupsParams{UserID: userID, Column2: pgtype.Text{String: q, Valid: true}})
	var groups []map[string]any
	for _, g := range gRows {
		groups = append(groups, map[string]any{"id": g.ID, "name": g.Name})
	}
	if groups == nil {
		groups = []map[string]any{}
	}
	eRows, _ := queries.SearchExpenses(r.Context(), db.SearchExpensesParams{UserID: userID, Column2: pgtype.Text{String: q, Valid: true}})
	var expenses []map[string]any
	for _, e := range eRows {
		expenses = append(expenses, map[string]any{"id": e.ID, "group_id": e.GroupID, "description": e.Description, "amount": e.Amount, "currency": e.Currency})
	}
	if expenses == nil {
		expenses = []map[string]any{}
	}
	res := map[string]any{"users": users, "groups": groups, "expenses": expenses}
	if err := h.searchWallet(r.Context(), queries, userID, q, res); err != nil {
		// Old clients never saw a 500 here: keep the legacy groups, drop the wallet ones.
		slog.Error("search: wallet search failed", "error", err)
		for _, k := range walletGroups {
			res[k] = []any{}
		}
	}
	httpx.WriteJSON(w, 200, res)
}

// walletGroups are the wallet result groups, each capped at 10 and owner-scoped.
var walletGroups = []string{"transactions", "accounts", "loans", "planned", "budgets", "categories", "tags"}

func emptyResult() map[string]any {
	res := map[string]any{"users": []any{}, "groups": []any{}, "expenses": []any{}}
	for _, k := range walletGroups {
		res[k] = []any{}
	}
	return res
}

// maxQueryRunes bounds the wallet match pattern.
const maxQueryRunes = 100

// likePattern builds the wallet match pattern from q, cut at maxQueryRunes.
func likePattern(q string) string {
	if utf8.RuneCountInString(q) > maxQueryRunes {
		q = string([]rune(q)[:maxQueryRunes])
	}
	return httpx.LikeContains(q)
}

type tagOut struct {
	ID    uuid.UUID `json:"id"`
	Name  string    `json:"name"`
	Color string    `json:"color"`
}

func tsOrNil(t pgtype.Timestamptz) *time.Time {
	if !t.Valid {
		return nil
	}
	v := t.Time.UTC()
	return &v
}

func uuidOrNil(u pgtype.UUID) *uuid.UUID {
	if !u.Valid {
		return nil
	}
	id := uuid.UUID(u.Bytes)
	return &id
}

// searchWallet adds the wallet result groups (transactions, accounts, loans,
// planned, budgets, categories, tags) for userID to res.
func (h *Handler) searchWallet(ctx context.Context, q *db.Queries, userID uuid.UUID, term string, res map[string]any) error {
	pat := likePattern(term)

	txRows, err := q.SearchWalletTransactions(ctx, db.SearchWalletTransactionsParams{UserID: userID, Pattern: pat})
	if err != nil {
		return err
	}
	ids := make([]pgtype.UUID, len(txRows))
	for i, t := range txRows {
		ids[i] = pgtype.UUID{Bytes: t.ID, Valid: true}
	}
	tagRows := []db.ListTagsForTransactionsRow{}
	if len(ids) > 0 {
		if tagRows, err = q.ListTagsForTransactions(ctx, ids); err != nil {
			return err
		}
	}
	tags := map[uuid.UUID][]tagOut{}
	for _, tr := range tagRows {
		id := uuid.UUID(tr.TransactionID.Bytes)
		tags[id] = append(tags[id], tagOut{ID: tr.ID, Name: tr.Name, Color: tr.Color})
	}
	txs := make([]map[string]any, 0, len(txRows))
	for _, t := range txRows {
		tg := tags[t.ID]
		if tg == nil {
			tg = []tagOut{}
		}
		var accName *string
		if t.AccountName.Valid {
			accName = &t.AccountName.String
		}
		var cat *uuid.UUID
		if t.CategoryID != uuid.Nil {
			c := t.CategoryID
			cat = &c
		}
		txs = append(txs, map[string]any{
			"id": t.ID, "type": t.Type, "title": t.Title, "description": t.Description,
			"amount": t.Amount, "currency": strings.TrimSpace(t.Currency),
			"account_id": uuidOrNil(t.AccountID), "account_name": accName, "category_id": cat,
			"occurred_at": tsOrNil(t.OccurredAt), "due_at": tsOrNil(t.DueAt),
			"planned": !t.OccurredAt.Valid, "source": t.Source, "tags": tg,
		})
	}
	res["transactions"] = txs

	accRows, err := q.SearchWalletAccounts(ctx, db.SearchWalletAccountsParams{UserID: userID, Pattern: pat})
	if err != nil {
		return err
	}
	accs := make([]map[string]any, 0, len(accRows))
	for _, a := range accRows {
		accs = append(accs, map[string]any{"id": a.ID, "name": a.Name, "currency": strings.TrimSpace(a.Currency), "color": a.Color, "icon": a.Icon})
	}
	res["accounts"] = accs

	loanRows, err := q.SearchWalletLoans(ctx, db.SearchWalletLoansParams{UserID: userID, Pattern: pat})
	if err != nil {
		return err
	}
	loanIDs := make([]pgtype.UUID, len(loanRows))
	for i, l := range loanRows {
		loanIDs[i] = pgtype.UUID{Bytes: l.ID, Valid: true}
	}
	type totals struct{ inc, dec int64 }
	tot := map[uuid.UUID]totals{}
	if len(loanIDs) > 0 {
		trs, err := q.LoanRecordTotals(ctx, loanIDs)
		if err != nil {
			return err
		}
		for _, tr := range trs {
			tot[uuid.UUID(tr.LoanID.Bytes)] = totals{tr.Increased, tr.Decreased}
		}
	}
	loans := make([]map[string]any, 0, len(loanRows))
	for _, l := range loanRows {
		t := tot[l.ID]
		total := l.Amount + t.inc
		remaining := max(total-t.dec, 0)
		loans = append(loans, map[string]any{
			"id": l.ID, "name": l.Name, "type": l.Type, "amount": l.Amount, "currency": strings.TrimSpace(l.Currency),
			"remaining": remaining, "paid_off": t.dec >= total,
		})
	}
	res["loans"] = loans

	plRows, err := q.SearchWalletPlanned(ctx, db.SearchWalletPlannedParams{UserID: userID, Pattern: pat})
	if err != nil {
		return err
	}
	planned := make([]map[string]any, 0, len(plRows))
	for _, p := range plRows {
		var n *int32
		if p.IntervalN.Valid {
			n = &p.IntervalN.Int32
		}
		var unit *string
		if p.IntervalUnit.Valid {
			unit = &p.IntervalUnit.String
		}
		planned = append(planned, map[string]any{
			"id": p.ID, "title": p.Title, "type": p.Type, "amount": p.Amount, "currency": strings.TrimSpace(p.Currency),
			"next_due_at": tsOrNil(p.NextDueAt), "one_time": p.OneTime, "interval_n": n, "interval_unit": unit, "active": p.Active,
		})
	}
	res["planned"] = planned

	bRows, err := q.SearchWalletBudgets(ctx, db.SearchWalletBudgetsParams{UserID: userID, Pattern: pat})
	if err != nil {
		return err
	}
	budgets := make([]map[string]any, 0, len(bRows))
	for _, b := range bRows {
		budgets = append(budgets, map[string]any{"id": b.ID, "name": b.Name, "amount": b.Amount, "currency": strings.TrimSpace(b.Currency)})
	}
	res["budgets"] = budgets

	cRows, err := q.SearchWalletCategories(ctx, db.SearchWalletCategoriesParams{UserID: userID, Pattern: pat})
	if err != nil {
		return err
	}
	cats := make([]map[string]any, 0, len(cRows))
	for _, c := range cRows {
		cats = append(cats, map[string]any{"id": c.ID, "name": c.Name, "icon": c.Icon, "color": c.Color})
	}
	res["categories"] = cats

	tRows, err := q.SearchWalletTags(ctx, db.SearchWalletTagsParams{UserID: userID, Pattern: pat})
	if err != nil {
		return err
	}
	tgs := make([]tagOut, 0, len(tRows))
	for _, t := range tRows {
		tgs = append(tgs, tagOut{ID: t.ID, Name: t.Name, Color: t.Color})
	}
	res["tags"] = tgs
	return nil
}
