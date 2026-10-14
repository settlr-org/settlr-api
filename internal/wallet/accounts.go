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

const (
	defaultAccountColor = "#6B7280"
	defaultAccountIcon  = "wallet"
)

func accountJSON(a db.Account, balance int64, balanceBase *int64, base string, missing []string) map[string]any {
	if missing == nil {
		missing = []string{}
	}
	return map[string]any{
		"missing_rates":      missing,
		"id":                 a.ID,
		"name":               a.Name,
		"currency":           strings.TrimSpace(a.Currency),
		"color":              a.Color,
		"icon":               a.Icon,
		"include_in_balance": a.IncludeInBalance,
		"is_default":         a.IsDefault,
		"sort_order":         a.SortOrder,
		"archived":           a.ArchivedAt.Valid,
		"archived_at":        tsPtr(a.ArchivedAt),
		"balance":            balance,
		"balance_base":       balanceBase,
		"base_currency":      base,
		"created_at":         a.CreatedAt.Time.UTC(),
		"updated_at":         a.UpdatedAt.Time.UTC(),
	}
}

// accountsJSON renders accounts with balances in their own and the base currency.
func (h *Handler) accountsJSON(ctx context.Context, userID uuid.UUID, accounts []db.Account) ([]map[string]any, *fx, error) {
	f, err := h.newFX(ctx, userID)
	if err != nil {
		return nil, nil, err
	}
	bals, missing, err := h.balances(ctx, userID, accounts, f)
	if err != nil {
		return nil, nil, err
	}
	out := make([]map[string]any, 0, len(accounts))
	for _, a := range accounts {
		b := bals[a.ID]
		var bb *int64
		if v, ok := f.convert(ctx, b, a.Currency, f.base); ok {
			bb = &v
		} else {
			missing[a.ID] = append(missing[a.ID], strings.TrimSpace(a.Currency)+"/"+f.base)
		}
		out = append(out, accountJSON(a, b, bb, f.base, missing[a.ID]))
	}
	return out, f, nil
}

func (h *Handler) ListAccounts(w http.ResponseWriter, r *http.Request) {
	userID := currentUser(r)
	accounts, err := h.q().ListAccounts(r.Context(), userID)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	out, _, err := h.accountsJSON(r.Context(), userID, accounts)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"data": out})
}

func (h *Handler) writeAccount(w http.ResponseWriter, r *http.Request, status int, a db.Account) {
	out, _, err := h.accountsJSON(r.Context(), a.UserID, []db.Account{a})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, status, out[0])
}

func (h *Handler) GetAccount(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	a, err := h.q().GetAccount(r.Context(), db.GetAccountParams{ID: id, UserID: currentUser(r)})
	if err != nil {
		httpx.WriteError(w, r, httpx.ErrNotFound)
		return
	}
	h.writeAccount(w, r, http.StatusOK, a)
}

type createAccountReq struct {
	Name             string `json:"name"`
	Currency         string `json:"currency"`
	Color            string `json:"color"`
	Icon             string `json:"icon"`
	IncludeInBalance *bool  `json:"include_in_balance"`
	Archived         bool   `json:"archived"`
	InitialBalance   int64  `json:"initial_balance"`
	IsDefault        bool   `json:"is_default"`
	SortOrder        *int32 `json:"sort_order"`
}

func (h *Handler) CreateAccount(w http.ResponseWriter, r *http.Request) {
	userID := currentUser(r)
	var req createAccountReq
	if !decode(w, r, &req) {
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" || len(req.Name) > 100 {
		validation(w, "name required (max 100)")
		return
	}
	ctx := r.Context()
	cur := normCurrency(req.Currency)
	if cur == "" {
		c, err := h.q().GetUserDefaultCurrency(ctx, userID)
		if err != nil {
			httpx.WriteError(w, r, err)
			return
		}
		cur = strings.TrimSpace(c)
	}
	if !validCurrency(cur) {
		validation(w, "unsupported currency")
		return
	}
	if req.Color == "" {
		req.Color = defaultAccountColor
	}
	if req.Icon == "" {
		req.Icon = defaultAccountIcon
	}
	include := true
	if req.IncludeInBalance != nil {
		include = *req.IncludeInBalance
	}
	var archived pgtype.Timestamptz
	if req.Archived {
		archived = pgtype.Timestamptz{Time: time.Now().UTC(), Valid: true}
	}

	tx, err := h.Pool.Begin(ctx)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := h.q().WithTx(tx)
	if err := qtx.LockUserWallet(ctx, userID.String()); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	_, defErr := qtx.GetDefaultAccount(ctx, userID)
	hasDefault := defErr == nil
	if defErr != nil && !isNoRows(defErr) {
		httpx.WriteError(w, r, defErr)
		return
	}
	if req.IsDefault && hasDefault {
		if err := qtx.ClearDefaultAccount(ctx, userID); err != nil {
			httpx.WriteError(w, r, err)
			return
		}
	}
	sortOrder := int32(0)
	if req.SortOrder != nil {
		sortOrder = *req.SortOrder
	} else if sortOrder, err = qtx.NextAccountSortOrder(ctx, userID); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	a, err := qtx.CreateAccount(ctx, db.CreateAccountParams{
		UserID: userID, Name: req.Name, Currency: cur, Color: req.Color, Icon: req.Icon,
		IncludeInBalance: include, IsDefault: req.IsDefault || !hasDefault, SortOrder: sortOrder, ArchivedAt: archived,
	})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if req.InitialBalance != 0 {
		if _, err := postAdjustment(ctx, qtx, a, req.InitialBalance, "Initial balance"); err != nil {
			httpx.WriteError(w, r, err)
			return
		}
	}
	if err := tx.Commit(ctx); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	h.writeAccount(w, r, http.StatusCreated, a)
}

// postAdjustment records an ADJUSTMENT transaction moving the account by delta.
func postAdjustment(ctx context.Context, q *db.Queries, a db.Account, delta int64, title string) (db.Transaction, error) {
	typ, amount := "INCOME", delta
	if delta < 0 {
		typ, amount = "EXPENSE", -delta
	}
	now := time.Now().UTC()
	return q.CreateTransaction(ctx, db.CreateTransactionParams{
		ID: uuid.New(), UserID: a.UserID, Type: typ, AccountID: pgUUID(&a.ID), Amount: amount,
		Currency: strings.TrimSpace(a.Currency), Title: title, OccurredAt: ts(&now), Source: "ADJUSTMENT",
	})
}

type updateAccountReq struct {
	Name             Opt[string] `json:"name"`
	Currency         Opt[string] `json:"currency"`
	Color            Opt[string] `json:"color"`
	Icon             Opt[string] `json:"icon"`
	IncludeInBalance Opt[bool]   `json:"include_in_balance"`
	Archived         Opt[bool]   `json:"archived"`
	IsDefault        Opt[bool]   `json:"is_default"`
}

func (h *Handler) UpdateAccount(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	userID := currentUser(r)
	ctx := r.Context()
	a, err := h.q().GetAccount(ctx, db.GetAccountParams{ID: id, UserID: userID})
	if err != nil {
		httpx.WriteError(w, r, httpx.ErrNotFound)
		return
	}
	var req updateAccountReq
	if !decode(w, r, &req) {
		return
	}
	p := db.UpdateAccountParams{
		ID: a.ID, UserID: userID, Name: a.Name, Currency: strings.TrimSpace(a.Currency), Color: a.Color, Icon: a.Icon,
		IncludeInBalance: a.IncludeInBalance, ArchivedAt: a.ArchivedAt,
	}
	if v, ok := req.Name.Value(); ok {
		v = strings.TrimSpace(v)
		if v == "" || len(v) > 100 {
			validation(w, "name required (max 100)")
			return
		}
		p.Name = v
	}
	if v, ok := req.Color.Value(); ok && v != "" {
		p.Color = v
	}
	if v, ok := req.Icon.Value(); ok && v != "" {
		p.Icon = v
	}
	if v, ok := req.IncludeInBalance.Value(); ok {
		p.IncludeInBalance = v
	}
	if v, ok := req.Archived.Value(); ok {
		if v && !a.ArchivedAt.Valid {
			p.ArchivedAt = pgtype.Timestamptz{Time: time.Now().UTC(), Valid: true}
		} else if !v {
			p.ArchivedAt = pgtype.Timestamptz{}
		}
	}
	if v, ok := req.Currency.Value(); ok {
		c := normCurrency(v)
		if !validCurrency(c) {
			validation(w, "unsupported currency")
			return
		}
		if c != p.Currency {
			busy, err := h.q().AccountHasActivity(ctx, pu(a.ID))
			if err != nil {
				httpx.WriteError(w, r, err)
				return
			}
			if busy {
				validation(w, "currency can only be changed on an account without transactions")
				return
			}
			p.Currency = c
		}
	}
	if v, ok := req.IsDefault.Value(); ok && !v && a.IsDefault {
		validation(w, "mark another account as default instead")
		return
	}
	tx, err := h.Pool.Begin(ctx)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := h.q().WithTx(tx)
	if v, ok := req.IsDefault.Value(); ok && v && !a.IsDefault {
		if err := qtx.ClearDefaultAccount(ctx, userID); err != nil {
			httpx.WriteError(w, r, err)
			return
		}
		if err := qtx.MarkDefaultAccount(ctx, db.MarkDefaultAccountParams{ID: a.ID, UserID: userID}); err != nil {
			httpx.WriteError(w, r, err)
			return
		}
	}
	updated, err := qtx.UpdateAccount(ctx, p)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if err := tx.Commit(ctx); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	h.writeAccount(w, r, http.StatusOK, updated)
}

// DeleteAccount soft-deletes the account and its transactions and unlinks it from
// group expenses and settlements. If it was the default, the next account takes over.
func (h *Handler) DeleteAccount(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	userID := currentUser(r)
	ctx := r.Context()
	tx, err := h.Pool.Begin(ctx)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := h.q().WithTx(tx)
	n, err := qtx.SoftDeleteAccount(ctx, db.SoftDeleteAccountParams{ID: id, UserID: userID})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if n == 0 {
		httpx.WriteError(w, r, httpx.ErrNotFound)
		return
	}
	steps := []func() error{
		func() error {
			return qtx.SoftDeleteAccountTransactions(ctx, db.SoftDeleteAccountTransactionsParams{UserID: userID, AccountID: pu(id)})
		},
		func() error { return qtx.ClearExpensePayerAccount(ctx, pgUUID(&id)) },
		func() error { return qtx.ClearSettlementFromAccount(ctx, pgUUID(&id)) },
		func() error { return qtx.DeleteSettlementAccountsByAccount(ctx, pu(id)) },
		func() error { return qtx.PromoteDefaultAccount(ctx, userID) },
	}
	for _, step := range steps {
		if err := step(); err != nil {
			httpx.WriteError(w, r, err)
			return
		}
	}
	if err := tx.Commit(ctx); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"message": "deleted"})
}

func (h *Handler) ReorderAccounts(w http.ResponseWriter, r *http.Request) {
	userID := currentUser(r)
	var req struct {
		IDs []uuid.UUID `json:"ids"`
	}
	if !decode(w, r, &req) {
		return
	}
	if len(req.IDs) == 0 || len(uniqueUUIDs(req.IDs)) != len(req.IDs) {
		validation(w, "ids must be a non-empty list of distinct account ids")
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
		n, err := qtx.SetAccountSortOrder(ctx, db.SetAccountSortOrderParams{ID: id, UserID: userID, SortOrder: int32(i)})
		if err != nil {
			httpx.WriteError(w, r, err)
			return
		}
		if n == 0 {
			validation(w, "unknown account id "+id.String())
			return
		}
	}
	if err := tx.Commit(ctx); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	h.ListAccounts(w, r)
}

// AdjustAccount posts an ADJUSTMENT so the account balance becomes the target.
func (h *Handler) AdjustAccount(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	userID := currentUser(r)
	ctx := r.Context()
	a, err := h.q().GetAccount(ctx, db.GetAccountParams{ID: id, UserID: userID})
	if err != nil {
		httpx.WriteError(w, r, httpx.ErrNotFound)
		return
	}
	var req struct {
		Balance *int64 `json:"balance"`
		Title   string `json:"title"`
	}
	if !decode(w, r, &req) {
		return
	}
	if req.Balance == nil {
		validation(w, "balance required (minor units)")
		return
	}
	f, err := h.newFX(ctx, userID)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	bals, missing, err := h.balances(ctx, userID, []db.Account{a}, f)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if len(missing[a.ID]) > 0 {
		// Part of the balance (a group entry in another currency) can't be converted,
		// so the difference to post would be wrong.
		writeErr(w, http.StatusUnprocessableEntity, "RATES_UNAVAILABLE",
			"current balance is incomplete: no exchange rate for "+strings.Join(missing[a.ID], ", ")+"; set one via PUT /me/rates/{base}/{quote}")
		return
	}
	delta := *req.Balance - bals[a.ID]
	var txn any
	if delta != 0 {
		title := strings.TrimSpace(req.Title)
		if title == "" {
			title = "Balance adjustment"
		}
		t, err := postAdjustment(ctx, h.q(), a, delta, title)
		if err != nil {
			httpx.WriteError(w, r, err)
			return
		}
		txn = transactionJSON(t, nil)
	}
	out, _, err := h.accountsJSON(ctx, userID, []db.Account{a})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"account": out[0], "transaction": txn})
}

// AccountSummary reports an account's flows in [from, to) in the account currency.
// income/expense count INCOME/EXPENSE transactions (plus transfers when the user
// treats transfers as income/expense); inflow/outflow count every movement.
func (h *Handler) AccountSummary(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	userID := currentUser(r)
	ctx := r.Context()
	a, err := h.q().GetAccount(ctx, db.GetAccountParams{ID: id, UserID: userID})
	if err != nil {
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
	settings, err := h.loadSettings(ctx, userID)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	f, err := h.newFX(ctx, userID)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	rows, err := h.q().AccountMovements(ctx, db.AccountMovementsParams{UserID: userID, AccountID: pu(a.ID), FromAt: ts(from), ToAt: ts(to)})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	cur := strings.TrimSpace(a.Currency)
	var income, expense, inflow, outflow int64
	add := func(dst *int64, v int64, c string) {
		if x, ok := f.convert(ctx, v, c, cur); ok {
			*dst += x
		}
	}
	for _, row := range rows {
		add(&income, row.Income, row.Currency)
		add(&expense, row.Expense, row.Currency)
		add(&inflow, row.Inflow, row.Currency)
		add(&outflow, row.Outflow, row.Currency)
		if settings.TreatTransfersAsFlow {
			add(&income, row.TransferIn, row.Currency)
			add(&expense, row.TransferOut, row.Currency)
		}
	}
	bals, _, err := h.balances(ctx, userID, []db.Account{a}, f)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	limit := queryLimit(r, 50, 200)
	entries, next, err := h.ledgerPage(ctx, userID, db.ListLedgerEntriesParams{
		UserID: userID, Planned: "exclude", FromAt: ts(from), ToAt: ts(to),
		AccountIds: pgUUIDs([]uuid.UUID{a.ID}), CursorAt: cursorAt, CursorID: cursorID, Lim: int32(limit + 1),
	}, limit)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	resp := map[string]any{
		"account_id": a.ID, "currency": cur, "from": from, "to": to,
		"balance": bals[a.ID], "income": income, "expense": expense, "inflow": inflow, "outflow": outflow,
		"entries": entries, "missing_rates": f.missingList(),
	}
	if next != nil {
		resp["next_cursor"] = *next
	}
	httpx.WriteJSON(w, http.StatusOK, resp)
}

// AccountForCurrency returns the account the legacy /personal API books an expense
// in `currency` to: the default account when currencies match (creating a "Cash"
// default in the user's currency if the user has no account yet), else the first
// account in that currency, else a new "Cash XXX" account. Empty currency means the
// default account.
//
// q must be bound to a transaction: a per-user advisory lock held until commit
// keeps concurrent first requests from racing to create the same accounts.
func AccountForCurrency(ctx context.Context, q *db.Queries, userID uuid.UUID, currency string) (db.Account, error) {
	if err := q.LockUserWallet(ctx, userID.String()); err != nil {
		return db.Account{}, err
	}
	def, err := ensureDefaultAccount(ctx, q, userID)
	if err != nil {
		return db.Account{}, err
	}
	currency = normCurrency(currency)
	if currency == "" || strings.TrimSpace(def.Currency) == currency {
		return def, nil
	}
	a, err := q.GetFirstAccountByCurrency(ctx, db.GetFirstAccountByCurrencyParams{UserID: userID, Currency: currency})
	if err == nil {
		return a, nil
	}
	if !isNoRows(err) {
		return db.Account{}, err
	}
	next, err := q.NextAccountSortOrder(ctx, userID)
	if err != nil {
		return db.Account{}, err
	}
	return q.CreateAccount(ctx, db.CreateAccountParams{
		UserID: userID, Name: "Cash " + currency, Currency: currency, Color: defaultAccountColor,
		Icon: defaultAccountIcon, IncludeInBalance: true, SortOrder: next,
	})
}

// ensureDefaultAccount expects the caller to hold the LockUserWallet lock.
func ensureDefaultAccount(ctx context.Context, q *db.Queries, userID uuid.UUID) (db.Account, error) {
	def, err := q.GetDefaultAccount(ctx, userID)
	if err == nil || !isNoRows(err) {
		return def, err
	}
	// Existing accounts but no default (e.g. it was deleted): promote one.
	if err := q.PromoteDefaultAccount(ctx, userID); err != nil {
		return db.Account{}, err
	}
	if def, err := q.GetDefaultAccount(ctx, userID); err == nil || !isNoRows(err) {
		return def, err
	}
	cur, err := q.GetUserDefaultCurrency(ctx, userID)
	if err != nil {
		return db.Account{}, err
	}
	return q.CreateAccount(ctx, db.CreateAccountParams{
		UserID: userID, Name: "Cash", Currency: strings.TrimSpace(cur), Color: defaultAccountColor,
		Icon: defaultAccountIcon, IncludeInBalance: true, IsDefault: true,
	})
}
