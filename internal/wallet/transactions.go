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
	maxTitleLen       = 200
	maxDescriptionLen = 2000
	maxAmount         = int64(1) << 53
)

type tagRef struct {
	ID    uuid.UUID `json:"id"`
	Name  string    `json:"name"`
	Color string    `json:"color"`
	Icon  string    `json:"icon"`
}

func transactionJSON(t db.Transaction, tags []tagRef) map[string]any {
	if tags == nil {
		tags = []tagRef{}
	}
	return map[string]any{
		"id":                 t.ID,
		"type":               t.Type,
		"account_id":         uuidPtr(t.AccountID),
		"to_account_id":      uuidPtr(t.ToAccountID),
		"amount":             t.Amount,
		"currency":           strings.TrimSpace(t.Currency),
		"to_amount":          int8Ptr(t.ToAmount),
		"category_id":        nilIfZero(t.CategoryID),
		"title":              t.Title,
		"description":        t.Description,
		"occurred_at":        tsPtr(t.OccurredAt),
		"due_at":             tsPtr(t.DueAt),
		"planned":            !t.OccurredAt.Valid,
		"planned_payment_id": uuidPtr(t.PlannedPaymentID),
		"loan_id":            uuidPtr(t.LoanID),
		"loan_record_id":     uuidPtr(t.LoanRecordID),
		"source":             t.Source,
		"tags":               tags,
		"created_at":         t.CreatedAt.Time.UTC(),
		"updated_at":         t.UpdatedAt.Time.UTC(),
	}
}

// tagsFor loads the tags of the given transactions.
func (h *Handler) tagsFor(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID][]tagRef, error) {
	out := map[uuid.UUID][]tagRef{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := h.q().ListTagsForTransactions(ctx, pgUUIDs(ids))
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		tid := uuid.UUID(row.TransactionID.Bytes)
		out[tid] = append(out[tid], tagRef{ID: row.ID, Name: row.Name, Color: row.Color, Icon: row.Icon})
	}
	return out, nil
}

func (h *Handler) writeTransaction(w http.ResponseWriter, r *http.Request, status int, t db.Transaction) {
	tags, err := h.tagsFor(r.Context(), []uuid.UUID{t.ID})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, status, transactionJSON(t, tags[t.ID]))
}

// txnInput is the full, merged state of a transaction to validate and store.
type txnInput struct {
	Type        string
	AccountID   *uuid.UUID
	ToAccountID *uuid.UUID
	Amount      int64
	ToAmount    *int64
	Currency    string
	CategoryID  *uuid.UUID
	Title       string
	Description string
	OccurredAt  *time.Time
	DueAt       *time.Time
	TagIDs      []uuid.UUID // nil = leave tags unchanged (PATCH)
}

// validate checks the wallet rules and normalises currency/to_amount. It returns
// a client-facing message on failure.
func (h *Handler) validate(ctx context.Context, userID uuid.UUID, in *txnInput) (string, error) {
	in.Type = strings.ToUpper(strings.TrimSpace(in.Type))
	if in.Type != "INCOME" && in.Type != "EXPENSE" && in.Type != "TRANSFER" {
		return "type must be INCOME, EXPENSE or TRANSFER", nil
	}
	if in.Amount <= 0 || in.Amount > maxAmount {
		return "amount must be > 0 (minor units)", nil
	}
	in.Title = strings.TrimSpace(in.Title)
	if len(in.Title) > maxTitleLen {
		return "title too long (max 200)", nil
	}
	if len(in.Description) > maxDescriptionLen {
		return "description too long (max 2000)", nil
	}
	if in.OccurredAt == nil && in.DueAt == nil {
		return "occurred_at or due_at required", nil
	}
	in.Currency = normCurrency(in.Currency)
	if in.AccountID == nil {
		if in.OccurredAt != nil || in.Type == "TRANSFER" {
			return "account_id required", nil
		}
		if !validCurrency(in.Currency) {
			return "currency required for a planned item without account", nil
		}
	} else {
		a, err := h.q().GetAccount(ctx, db.GetAccountParams{ID: *in.AccountID, UserID: userID})
		if err != nil {
			if isNoRows(err) {
				return "account_id not found", nil
			}
			return "", err
		}
		ac := strings.TrimSpace(a.Currency)
		if in.Currency != "" && in.Currency != ac {
			return "currency must match the account currency (" + ac + ")", nil
		}
		in.Currency = ac
	}
	if in.Type == "TRANSFER" {
		if in.ToAccountID == nil {
			return "to_account_id required for TRANSFER", nil
		}
		if *in.ToAccountID == *in.AccountID {
			return "to_account_id must differ from account_id", nil
		}
		to, err := h.q().GetAccount(ctx, db.GetAccountParams{ID: *in.ToAccountID, UserID: userID})
		if err != nil {
			if isNoRows(err) {
				return "to_account_id not found", nil
			}
			return "", err
		}
		if strings.TrimSpace(to.Currency) == in.Currency {
			if in.ToAmount != nil && *in.ToAmount != in.Amount {
				return "to_amount must equal amount for a same-currency transfer", nil
			}
			amt := in.Amount
			in.ToAmount = &amt
		} else if in.ToAmount == nil || *in.ToAmount <= 0 || *in.ToAmount > maxAmount {
			return "to_amount > 0 required when the accounts' currencies differ", nil
		}
	} else if in.ToAccountID != nil || in.ToAmount != nil {
		return "to_account_id and to_amount are only allowed for TRANSFER", nil
	}
	if in.CategoryID != nil {
		ok, err := h.q().CategoryVisible(ctx, db.CategoryVisibleParams{ID: pu(*in.CategoryID), UserID: pu(userID)})
		if err != nil {
			return "", err
		}
		if !ok {
			return "category_id not found", nil
		}
	}
	if in.TagIDs != nil {
		in.TagIDs = uniqueUUIDs(in.TagIDs)
		if len(in.TagIDs) > 0 {
			n, err := h.q().CountOwnedTags(ctx, db.CountOwnedTagsParams{UserID: userID, Ids: pgUUIDs(in.TagIDs)})
			if err != nil {
				return "", err
			}
			if int(n) != len(in.TagIDs) {
				return "tag_ids contains an unknown tag", nil
			}
		}
	}
	return "", nil
}

type createTxnReq struct {
	Type        string      `json:"type"`
	AccountID   *uuid.UUID  `json:"account_id"`
	ToAccountID *uuid.UUID  `json:"to_account_id"`
	Amount      int64       `json:"amount"`
	ToAmount    *int64      `json:"to_amount"`
	Currency    string      `json:"currency"`
	CategoryID  *uuid.UUID  `json:"category_id"`
	Title       string      `json:"title"`
	Description string      `json:"description"`
	OccurredAt  *string     `json:"occurred_at"`
	DueAt       *string     `json:"due_at"`
	TagIDs      []uuid.UUID `json:"tag_ids"`
}

func optTime(s *string) (*time.Time, string) {
	if s == nil || strings.TrimSpace(*s) == "" {
		return nil, ""
	}
	t, _, err := parseTime(*s)
	if err != nil {
		return nil, err.Error()
	}
	return &t, ""
}

func (h *Handler) CreateTransaction(w http.ResponseWriter, r *http.Request) {
	userID := currentUser(r)
	var req createTxnReq
	if !decode(w, r, &req) {
		return
	}
	occurred, msg := optTime(req.OccurredAt)
	if msg != "" {
		validation(w, "occurred_at: "+msg)
		return
	}
	due, msg := optTime(req.DueAt)
	if msg != "" {
		validation(w, "due_at: "+msg)
		return
	}
	if occurred == nil && due == nil {
		now := time.Now().UTC()
		occurred = &now
	}
	tagIDs := req.TagIDs
	if tagIDs == nil {
		tagIDs = []uuid.UUID{}
	}
	in := txnInput{
		Type: req.Type, AccountID: req.AccountID, ToAccountID: req.ToAccountID, Amount: req.Amount,
		ToAmount: req.ToAmount, Currency: req.Currency, CategoryID: req.CategoryID, Title: req.Title,
		Description: req.Description, OccurredAt: occurred, DueAt: due, TagIDs: tagIDs,
	}
	h.insertTransaction(w, r, userID, in, "MANUAL")
}

func (h *Handler) insertTransaction(w http.ResponseWriter, r *http.Request, userID uuid.UUID, in txnInput, source string) {
	ctx := r.Context()
	msg, err := h.validate(ctx, userID, &in)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if msg != "" {
		validation(w, msg)
		return
	}
	tx, err := h.Pool.Begin(ctx)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := h.q().WithTx(tx)
	t, err := qtx.CreateTransaction(ctx, db.CreateTransactionParams{
		ID: uuid.New(), UserID: userID, Type: in.Type, AccountID: pgUUID(in.AccountID), ToAccountID: pgUUID(in.ToAccountID),
		Amount: in.Amount, Currency: in.Currency, ToAmount: pgInt8(in.ToAmount), CategoryID: pgUUID(in.CategoryID),
		Title: in.Title, Description: in.Description, OccurredAt: ts(in.OccurredAt), DueAt: ts(in.DueAt), Source: source,
	})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if len(in.TagIDs) > 0 {
		if err := qtx.AddTransactionTags(ctx, db.AddTransactionTagsParams{TransactionID: pu(t.ID), TagIds: pgUUIDs(in.TagIDs)}); err != nil {
			httpx.WriteError(w, r, err)
			return
		}
	}
	if err := tx.Commit(ctx); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	h.writeTransaction(w, r, http.StatusCreated, t)
}

func (h *Handler) GetTransaction(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	t, err := h.q().GetTransaction(r.Context(), db.GetTransactionParams{ID: id, UserID: currentUser(r)})
	if err != nil {
		httpx.WriteError(w, r, httpx.ErrNotFound)
		return
	}
	h.writeTransaction(w, r, http.StatusOK, t)
}

type updateTxnReq struct {
	Type        Opt[string]      `json:"type"`
	AccountID   Opt[uuid.UUID]   `json:"account_id"`
	ToAccountID Opt[uuid.UUID]   `json:"to_account_id"`
	Amount      Opt[int64]       `json:"amount"`
	ToAmount    Opt[int64]       `json:"to_amount"`
	Currency    Opt[string]      `json:"currency"`
	CategoryID  Opt[uuid.UUID]   `json:"category_id"`
	Title       Opt[string]      `json:"title"`
	Description Opt[string]      `json:"description"`
	OccurredAt  Opt[string]      `json:"occurred_at"`
	DueAt       Opt[string]      `json:"due_at"`
	TagIDs      Opt[[]uuid.UUID] `json:"tag_ids"`
}

// UpdateTransaction applies a partial update: absent fields keep their value, null
// clears nullable ones (category_id, to_account_id, to_amount, occurred_at, due_at).
func (h *Handler) UpdateTransaction(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	userID := currentUser(r)
	ctx := r.Context()
	cur, err := h.q().GetTransaction(ctx, db.GetTransactionParams{ID: id, UserID: userID})
	if err != nil {
		httpx.WriteError(w, r, httpx.ErrNotFound)
		return
	}
	if cur.Source == "LOAN" {
		loanManaged(w)
		return
	}
	var req updateTxnReq
	if !decode(w, r, &req) {
		return
	}
	in := txnInput{
		Type: cur.Type, AccountID: uuidPtr(cur.AccountID), ToAccountID: uuidPtr(cur.ToAccountID), Amount: cur.Amount,
		ToAmount: int8Ptr(cur.ToAmount), Currency: "", CategoryID: nilIfZero(cur.CategoryID), Title: cur.Title,
		Description: cur.Description, OccurredAt: tsPtr(cur.OccurredAt), DueAt: tsPtr(cur.DueAt),
	}
	if in.AccountID == nil {
		in.Currency = cur.Currency
	}
	if req.Type.Set {
		in.Type = req.Type.V
		// Leaving TRANSFER drops the transfer-only fields unless they are re-sent.
		if strings.ToUpper(in.Type) != "TRANSFER" {
			if !req.ToAccountID.Set {
				in.ToAccountID = nil
			}
			if !req.ToAmount.Set {
				in.ToAmount = nil
			}
		}
	}
	if req.AccountID.Set {
		in.AccountID = nil
		if v, ok := req.AccountID.Value(); ok {
			in.AccountID = &v
		}
		if !req.ToAmount.Set && in.Type == "TRANSFER" {
			in.ToAmount = nil // recomputed or required again for the new pair
		}
	}
	if req.ToAccountID.Set {
		in.ToAccountID = nil
		if v, ok := req.ToAccountID.Value(); ok {
			in.ToAccountID = &v
		}
		if !req.ToAmount.Set {
			in.ToAmount = nil
		}
	}
	if v, ok := req.Amount.Value(); ok {
		in.Amount = v
		if !req.ToAmount.Set && in.ToAmount != nil && cur.ToAmount.Valid && cur.ToAmount.Int64 == cur.Amount {
			in.ToAmount = nil // same-currency transfer: follow the new amount
		}
	}
	if req.ToAmount.Set {
		in.ToAmount = nil
		if v, ok := req.ToAmount.Value(); ok {
			in.ToAmount = &v
		}
	}
	if v, ok := req.Currency.Value(); ok {
		in.Currency = v
	}
	if req.CategoryID.Set {
		in.CategoryID = nil
		if v, ok := req.CategoryID.Value(); ok {
			in.CategoryID = &v
		}
	}
	if v, ok := req.Title.Value(); ok {
		in.Title = v
	}
	if v, ok := req.Description.Value(); ok {
		in.Description = v
	}
	if req.OccurredAt.Set {
		in.OccurredAt = nil
		if v, ok := req.OccurredAt.Value(); ok {
			t, msg := optTime(&v)
			if msg != "" {
				validation(w, "occurred_at: "+msg)
				return
			}
			in.OccurredAt = t
		}
	}
	if req.DueAt.Set {
		in.DueAt = nil
		if v, ok := req.DueAt.Value(); ok {
			t, msg := optTime(&v)
			if msg != "" {
				validation(w, "due_at: "+msg)
				return
			}
			in.DueAt = t
		}
	}
	if req.TagIDs.Set {
		in.TagIDs = []uuid.UUID{}
		if v, ok := req.TagIDs.Value(); ok && v != nil {
			in.TagIDs = v
		}
	}
	if cur.PlannedPaymentID.Valid && cur.OccurredAt.Valid && in.OccurredAt == nil {
		validation(w, "a paid occurrence of a planned payment cannot be made unpaid again")
		return
	}
	msg, err := h.validate(ctx, userID, &in)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if msg != "" {
		validation(w, msg)
		return
	}
	tx, err := h.Pool.Begin(ctx)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := h.q().WithTx(tx)
	locked, err := qtx.LockTransaction(ctx, db.LockTransactionParams{ID: id, UserID: userID})
	if err != nil {
		httpx.WriteError(w, r, httpx.ErrNotFound)
		return
	}
	t, err := qtx.UpdateTransaction(ctx, db.UpdateTransactionParams{
		ID: id, UserID: userID, Type: in.Type, AccountID: pgUUID(in.AccountID), ToAccountID: pgUUID(in.ToAccountID),
		Amount: in.Amount, Currency: in.Currency, ToAmount: pgInt8(in.ToAmount), CategoryID: pgUUID(in.CategoryID),
		Title: in.Title, Description: in.Description, OccurredAt: ts(in.OccurredAt), DueAt: ts(in.DueAt),
	})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if in.TagIDs != nil {
		if err := qtx.DeleteTransactionTags(ctx, pu(id)); err != nil {
			httpx.WriteError(w, r, err)
			return
		}
		if len(in.TagIDs) > 0 {
			if err := qtx.AddTransactionTags(ctx, db.AddTransactionTagsParams{TransactionID: pu(id), TagIds: pgUUIDs(in.TagIDs)}); err != nil {
				httpx.WriteError(w, r, err)
				return
			}
		}
	}
	// Setting occurred_at on a plan's pending occurrence pays it: generate the next.
	if locked.PlannedPaymentID.Valid && !locked.OccurredAt.Valid && in.OccurredAt != nil {
		if _, err := h.advancePlan(ctx, qtx, uuid.UUID(locked.PlannedPaymentID.Bytes), userID, locked.DueAt.Time, false); err != nil {
			httpx.WriteError(w, r, err)
			return
		}
	}
	if err := tx.Commit(ctx); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	h.writeTransaction(w, r, http.StatusOK, t)
}

// DeleteTransaction soft-deletes a transaction. Deleting a plan's pending
// occurrence skips it (the next occurrence is generated); loan bookings can only
// be removed through the loan endpoints (409 LOAN_MANAGED).
func (h *Handler) DeleteTransaction(w http.ResponseWriter, r *http.Request) {
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
	cur, err := qtx.LockTransaction(ctx, db.LockTransactionParams{ID: id, UserID: userID})
	if err != nil {
		httpx.WriteError(w, r, httpx.ErrNotFound)
		return
	}
	if cur.Source == "LOAN" {
		loanManaged(w)
		return
	}
	if cur.OccurredAt.Valid {
		err = qtx.SoftDeleteTransactionByID(ctx, id)
	} else {
		_, err = h.skipOne(ctx, qtx, cur, false)
	}
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if err := tx.Commit(ctx); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"message": "deleted"})
}

// DuplicateTransaction copies a transaction (with its tags) as a new MANUAL one.
func (h *Handler) DuplicateTransaction(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	userID := currentUser(r)
	ctx := r.Context()
	cur, err := h.q().GetTransaction(ctx, db.GetTransactionParams{ID: id, UserID: userID})
	if err != nil {
		httpx.WriteError(w, r, httpx.ErrNotFound)
		return
	}
	if cur.Source == "LOAN" {
		loanManaged(w)
		return
	}
	tags, err := h.tagsFor(ctx, []uuid.UUID{id})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	tagIDs := []uuid.UUID{}
	for _, t := range tags[id] {
		tagIDs = append(tagIDs, t.ID)
	}
	in := txnInput{
		Type: cur.Type, AccountID: uuidPtr(cur.AccountID), ToAccountID: uuidPtr(cur.ToAccountID), Amount: cur.Amount,
		ToAmount: int8Ptr(cur.ToAmount), Currency: cur.Currency, CategoryID: nilIfZero(cur.CategoryID), Title: cur.Title,
		Description: cur.Description, OccurredAt: tsPtr(cur.OccurredAt), DueAt: tsPtr(cur.DueAt), TagIDs: tagIDs,
	}
	h.insertTransaction(w, r, userID, in, "MANUAL")
}

func (h *Handler) ListTransactions(w http.ResponseWriter, r *http.Request) {
	userID := currentUser(r)
	f, msg := parseFilters(r)
	if msg != "" {
		validation(w, msg)
		return
	}
	limit := queryLimit(r, 50, 200)
	ctx := r.Context()
	rows, err := h.q().ListTransactions(ctx, db.ListTransactionsParams{
		UserID: userID, Planned: f.planned, FromAt: f.from, ToAt: f.to, Types: f.types,
		AccountIds: f.accounts, CategoryIds: f.categories, TagIds: f.tags, ExclTagIds: f.exclTags,
		Q: f.q, ExclQ: f.exclQ, MinAmount: f.min, MaxAmount: f.max, CursorAt: f.cursorAt, CursorID: f.cursorID,
		Lim: int32(limit + 1),
	})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	var next *string
	if len(rows) > limit {
		rows = rows[:limit]
		last := rows[len(rows)-1]
		sortAt := last.OccurredAt
		if !sortAt.Valid {
			sortAt = last.DueAt
		}
		c := encodeCursor(sortAt.Time, last.ID)
		next = &c
	}
	ids := make([]uuid.UUID, len(rows))
	for i, t := range rows {
		ids[i] = t.ID
	}
	tags, err := h.tagsFor(ctx, ids)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	out := make([]map[string]any, 0, len(rows))
	for _, t := range rows {
		out = append(out, transactionJSON(t, tags[t.ID]))
	}
	resp := map[string]any{"data": out}
	if next != nil {
		resp["next_cursor"] = *next
	}
	httpx.WriteJSON(w, http.StatusOK, resp)
}

// filters are the shared query filters of /transactions and /ledger.
type filters struct {
	planned                            string
	from, to                           pgtype.Timestamptz
	types, kinds                       []string
	accounts, categories, groups, tags []pgtype.UUID
	exclTags                           []pgtype.UUID
	q, exclQ                           string
	min, max                           pgtype.Int8
	cursorAt                           pgtype.Timestamptz
	cursorID                           pgtype.UUID
}

func parseFilters(r *http.Request) (filters, string) {
	var f filters
	from, to, err := parseRange(r)
	if err != nil {
		return f, err.Error()
	}
	f.from, f.to = ts(from), ts(to)
	f.planned = plannedMode(r)
	f.types = queryList(r, "type")
	f.kinds = queryList(r, "kind")
	lists := []struct {
		name string
		dst  *[]pgtype.UUID
	}{
		{"account", &f.accounts}, {"category", &f.categories}, {"group", &f.groups},
		{"tag", &f.tags}, {"excl_tag", &f.exclTags},
	}
	for _, l := range lists {
		ids, err := queryUUIDs(r, l.name)
		if err != nil {
			return f, err.Error()
		}
		*l.dst = pgUUIDs(ids)
	}
	f.q = strings.TrimSpace(r.URL.Query().Get("q"))
	f.exclQ = strings.TrimSpace(r.URL.Query().Get("excl_q"))
	if f.min, err = queryInt64(r, "min"); err != nil {
		return f, err.Error()
	}
	if f.max, err = queryInt64(r, "max"); err != nil {
		return f, err.Error()
	}
	if f.cursorAt, f.cursorID, err = decodeCursor(r.URL.Query().Get("cursor")); err != nil {
		return f, err.Error()
	}
	return f, ""
}
