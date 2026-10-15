package wallet

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	db "github.com/settlr-org/settlr-api/internal/db"
	"github.com/settlr-org/settlr-api/internal/httpx"
)

// Loans (Ivy loans), kept distinct from friend balances. A loan is money lent
// (LEND) or borrowed (BORROW); records increase it or pay it down (DECREASE).
// A loan or record with an account posts a "booking" transaction (source LOAN)
// that moves the account balance but is never income or expense:
//
//	LEND   loan / INCREASE → EXPENSE (money out)   DECREASE → INCOME (money back)
//	BORROW loan / INCREASE → INCOME  (money in)    DECREASE → EXPENSE (paying back)
//
// Bookings are only changed through the loan endpoints (PATCH/DELETE
// /transactions/{id} on them → 409 LOAN_MANAGED). Amounts are in the loan
// currency; when the account has another currency the booking is converted
// (or uses account_amount when given).

const defaultLoanIcon = "loan"

func bookingType(loanType, recordType string) string {
	in := recordType == "DECREASE"
	if loanType == "BORROW" {
		in = !in
	}
	if in {
		return "INCOME"
	}
	return "EXPENSE"
}

type loanTotals struct {
	increased, decreased int64
	records              int32
}

func loanJSON(l db.Loan, t loanTotals, bookingID *uuid.UUID) map[string]any {
	total := l.Amount + t.increased
	remaining := total - t.decreased
	if remaining < 0 {
		remaining = 0
	}
	return map[string]any{
		"id":             l.ID,
		"name":           l.Name,
		"type":           l.Type,
		"amount":         l.Amount,
		"currency":       strings.TrimSpace(l.Currency),
		"color":          l.Color,
		"icon":           l.Icon,
		"note":           l.Note,
		"account_id":     uuidPtr(l.AccountID),
		"transaction_id": bookingID,
		"occurred_at":    l.OccurredAt.Time.UTC(),
		"sort_order":     l.SortOrder,
		"total":          total,
		"paid":           t.decreased,
		"remaining":      remaining,
		"percent":        percentOf(t.decreased, total),
		"paid_off":       t.decreased >= total,
		"records_count":  t.records,
		"created_at":     l.CreatedAt.Time.UTC(),
		"updated_at":     l.UpdatedAt.Time.UTC(),
	}
}

func loanRecordJSON(r db.LoanRecord) map[string]any {
	return map[string]any{
		"id":             r.ID,
		"loan_id":        uuidPtr(r.LoanID),
		"type":           r.Type,
		"amount":         r.Amount,
		"note":           r.Note,
		"occurred_at":    r.OccurredAt.Time.UTC(),
		"account_id":     uuidPtr(r.AccountID),
		"transaction_id": uuidPtr(r.TransactionID),
		"created_at":     r.CreatedAt.Time.UTC(),
		"updated_at":     r.UpdatedAt.Time.UTC(),
	}
}

// loansJSON renders loans with their record totals and booking transaction ids.
func (h *Handler) loansJSON(ctx context.Context, loans []db.Loan) ([]map[string]any, []loanTotals, error) {
	ids := make([]uuid.UUID, len(loans))
	for i, l := range loans {
		ids[i] = l.ID
	}
	rows, err := h.q().LoanRecordTotals(ctx, pgUUIDs(ids))
	if err != nil {
		return nil, nil, err
	}
	totals := map[uuid.UUID]loanTotals{}
	for _, row := range rows {
		totals[uuid.UUID(row.LoanID.Bytes)] = loanTotals{increased: row.Increased, decreased: row.Decreased, records: row.Records}
	}
	bookings, err := h.q().ListLoanBookings(ctx, pgUUIDs(ids))
	if err != nil {
		return nil, nil, err
	}
	bookingOf := map[uuid.UUID]uuid.UUID{}
	for _, t := range bookings {
		bookingOf[uuid.UUID(t.LoanID.Bytes)] = t.ID
	}
	out := make([]map[string]any, 0, len(loans))
	tots := make([]loanTotals, 0, len(loans))
	for _, l := range loans {
		var booking *uuid.UUID
		if id, ok := bookingOf[l.ID]; ok && l.AccountID.Valid {
			booking = &id
		}
		out = append(out, loanJSON(l, totals[l.ID], booking))
		tots = append(tots, totals[l.ID])
	}
	return out, tots, nil
}

func (h *Handler) writeLoan(w http.ResponseWriter, r *http.Request, status int, l db.Loan) {
	out, _, err := h.loansJSON(r.Context(), []db.Loan{l})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, status, out[0])
}

// booking describes the transaction a loan or record should have.
type booking struct {
	userID        uuid.UUID
	loanID        uuid.UUID
	recordID      *uuid.UUID
	accountID     *uuid.UUID
	typ           string
	amount        int64 // loan currency
	currency      string
	accountAmount *int64 // explicit amount in the account currency
	keepAmount    bool   // keep the existing booking's amount (nothing money-related changed)
	title, note   string
	occurredAt    time.Time
}

// syncBooking creates, updates or deletes the booking transaction. existing is
// the current booking (nil when none). It returns the booking's id (nil when the
// loan/record has no account) or a client-facing message.
func (h *Handler) syncBooking(ctx context.Context, q *db.Queries, b booking, existing *db.Transaction) (*uuid.UUID, string, error) {
	if b.accountID == nil {
		if existing != nil {
			return nil, "", q.SoftDeleteTransactionByID(ctx, existing.ID)
		}
		return nil, "", nil
	}
	a, err := q.GetAccount(ctx, db.GetAccountParams{ID: *b.accountID, UserID: b.userID})
	if isNoRows(err) {
		return nil, "account_id not found", nil
	}
	if err != nil {
		return nil, "", err
	}
	ac := strings.TrimSpace(a.Currency)
	amount := b.amount
	switch {
	case b.accountAmount != nil:
		if *b.accountAmount <= 0 || *b.accountAmount > maxAmount {
			return nil, "account_amount must be > 0 (minor units)", nil
		}
		amount = *b.accountAmount
	case b.keepAmount && existing != nil && existing.AccountID.Valid && uuid.UUID(existing.AccountID.Bytes) == a.ID:
		amount = existing.Amount
	case ac != b.currency:
		f, err := h.newFX(ctx, b.userID)
		if err != nil {
			return nil, "", err
		}
		v, ok := f.convert(ctx, b.amount, b.currency, ac)
		if !ok || v <= 0 {
			return nil, "no exchange rate " + b.currency + "/" + ac + "; send account_amount or set a rate via PUT /me/rates/{base}/{quote}", nil
		}
		amount = v
	}
	occurred := b.occurredAt
	if existing != nil {
		t, err := q.LoanBookingUpdate(ctx, db.LoanBookingUpdateParams{
			ID: existing.ID, Type: b.typ, AccountID: pu(a.ID), Amount: amount, Currency: ac,
			Title: b.title, Description: b.note, OccurredAt: ts(&occurred),
		})
		if err != nil {
			return nil, "", err
		}
		return &t.ID, "", nil
	}
	t, err := q.CreateTransaction(ctx, db.CreateTransactionParams{
		ID: uuid.New(), UserID: b.userID, Type: b.typ, AccountID: pu(a.ID), Amount: amount, Currency: ac,
		Title: b.title, Description: b.note, OccurredAt: ts(&occurred), LoanID: pu(b.loanID),
		LoanRecordID: pgUUID(b.recordID), Source: "LOAN",
	})
	if err != nil {
		return nil, "", err
	}
	return &t.ID, "", nil
}

func loanBookingTitle(l db.Loan) string { return l.Name }

// ---------------------------------------------------------------- loans

// ListLoans lists loans (filters: type=LEND|BORROW, include_paid_off=true|false,
// default true) with totals of what is still owed in the base currency.
func (h *Handler) ListLoans(w http.ResponseWriter, r *http.Request) {
	userID := currentUser(r)
	ctx := r.Context()
	includePaid := true
	switch strings.ToLower(strings.TrimSpace(r.URL.Query().Get("include_paid_off"))) {
	case "", "true", "1":
	case "false", "0":
		includePaid = false
	default:
		validation(w, "include_paid_off must be true or false")
		return
	}
	loans, err := h.q().ListLoans(ctx, db.ListLoansParams{UserID: userID, Types: queryList(r, "type")})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	out, totals, err := h.loansJSON(ctx, loans)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	f, err := h.newFX(ctx, userID)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	var lend, borrow int64
	data := make([]map[string]any, 0, len(out))
	for i, l := range loans {
		if !includePaid && out[i]["paid_off"].(bool) {
			continue
		}
		data = append(data, out[i])
		total := l.Amount + totals[i].increased
		remaining := total - totals[i].decreased
		if remaining <= 0 {
			continue
		}
		if v, ok := f.convert(ctx, remaining, l.Currency, f.base); ok {
			if l.Type == "LEND" {
				lend += v
			} else {
				borrow += v
			}
		}
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"data": data, "currency": f.base, "lend_remaining": lend, "borrow_remaining": borrow,
		"missing_rates": f.missingList(),
	})
}

func (h *Handler) GetLoan(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	l, err := h.q().GetLoan(r.Context(), db.GetLoanParams{ID: id, UserID: currentUser(r)})
	if err != nil {
		httpx.WriteError(w, r, httpx.ErrNotFound)
		return
	}
	h.writeLoan(w, r, http.StatusOK, l)
}

type loanReq struct {
	Name          Opt[string]    `json:"name"`
	Type          Opt[string]    `json:"type"`
	Amount        Opt[int64]     `json:"amount"`
	Currency      Opt[string]    `json:"currency"`
	Color         Opt[string]    `json:"color"`
	Icon          Opt[string]    `json:"icon"`
	Note          Opt[string]    `json:"note"`
	AccountID     Opt[uuid.UUID] `json:"account_id"`
	AccountAmount Opt[int64]     `json:"account_amount"`
	OccurredAt    Opt[string]    `json:"occurred_at"`
}

type loanState struct {
	name, typ, currency, color, icon, note string
	amount                                 int64
	accountID                              *uuid.UUID
	occurredAt                             time.Time
}

func (s *loanState) apply(req loanReq) string {
	if v, ok := req.Name.Value(); ok {
		s.name = v
	}
	if v, ok := req.Type.Value(); ok {
		s.typ = strings.ToUpper(strings.TrimSpace(v))
	}
	if v, ok := req.Amount.Value(); ok {
		s.amount = v
	}
	if v, ok := req.Currency.Value(); ok {
		s.currency = normCurrency(v)
	}
	if v, ok := req.Color.Value(); ok && v != "" {
		s.color = v
	}
	if v, ok := req.Icon.Value(); ok && v != "" {
		s.icon = v
	}
	if v, ok := req.Note.Value(); ok {
		s.note = v
	}
	if req.AccountID.Set {
		s.accountID = nil
		if v, ok := req.AccountID.Value(); ok {
			s.accountID = &v
		}
	}
	if v, ok := req.OccurredAt.Value(); ok {
		t, msg := optTime(&v)
		if msg != "" {
			return "occurred_at: " + msg
		}
		if t != nil {
			s.occurredAt = *t
		}
	}
	return ""
}

// validate checks the loan fields; currency defaults to the account's, then the
// user's base currency.
func (h *Handler) validateLoan(ctx context.Context, userID uuid.UUID, s *loanState) (string, error) {
	s.name = strings.TrimSpace(s.name)
	if s.name == "" || len(s.name) > 100 {
		return "name required (max 100)", nil
	}
	if s.typ != "LEND" && s.typ != "BORROW" {
		return "type must be LEND or BORROW", nil
	}
	if s.amount <= 0 || s.amount > maxAmount {
		return "amount must be > 0 (minor units)", nil
	}
	if len(s.note) > maxDescriptionLen {
		return "note too long (max 2000)", nil
	}
	if s.accountID != nil {
		a, err := h.q().GetAccount(ctx, db.GetAccountParams{ID: *s.accountID, UserID: userID})
		if isNoRows(err) {
			return "account_id not found", nil
		}
		if err != nil {
			return "", err
		}
		if s.currency == "" {
			s.currency = strings.TrimSpace(a.Currency)
		}
	}
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
	return "", nil
}

func (h *Handler) CreateLoan(w http.ResponseWriter, r *http.Request) {
	userID := currentUser(r)
	ctx := r.Context()
	var req loanReq
	if !decode(w, r, &req) {
		return
	}
	s := loanState{color: defaultAccountColor, icon: defaultLoanIcon, occurredAt: time.Now().UTC()}
	if msg := s.apply(req); msg != "" {
		validation(w, msg)
		return
	}
	msg, err := h.validateLoan(ctx, userID, &s)
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
	next, err := qtx.NextLoanSortOrder(ctx, userID)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	l, err := qtx.CreateLoan(ctx, db.CreateLoanParams{
		UserID: userID, Name: s.name, Type: s.typ, Amount: s.amount, Currency: s.currency, Color: s.color,
		Icon: s.icon, Note: s.note, AccountID: pgUUID(s.accountID), OccurredAt: ts(&s.occurredAt), SortOrder: next,
	})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	_, msg, err = h.syncBooking(ctx, qtx, booking{
		userID: userID, loanID: l.ID, accountID: s.accountID, typ: bookingType(s.typ, ""), amount: s.amount,
		currency: s.currency, accountAmount: optInt64(req.AccountAmount), title: loanBookingTitle(l), note: s.note, occurredAt: s.occurredAt,
	}, nil)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if msg != "" {
		validation(w, msg)
		return
	}
	if err := tx.Commit(ctx); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	h.writeLoan(w, r, http.StatusCreated, l)
}

// UpdateLoan applies a partial update (merged onto the locked row) and keeps the
// loan's booking and its records' bookings in sync (type, title, account, amount).
func (h *Handler) UpdateLoan(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	userID := currentUser(r)
	ctx := r.Context()
	if _, err := h.q().GetLoan(ctx, db.GetLoanParams{ID: id, UserID: userID}); err != nil {
		httpx.WriteError(w, r, httpx.ErrNotFound)
		return
	}
	var req loanReq
	if !decode(w, r, &req) {
		return
	}
	var l db.Loan
	err := h.withTx(ctx, func(qtx *db.Queries) error {
		cur, err := qtx.LockLoan(ctx, db.LockLoanParams{ID: id, UserID: userID})
		if isNoRows(err) {
			return httpx.ErrNotFound
		}
		if err != nil {
			return err
		}
		s := loanState{
			name: cur.Name, typ: cur.Type, currency: strings.TrimSpace(cur.Currency), color: cur.Color, icon: cur.Icon,
			note: cur.Note, amount: cur.Amount, accountID: uuidPtr(cur.AccountID), occurredAt: cur.OccurredAt.Time.UTC(),
		}
		if msg := s.apply(req); msg != "" {
			return vErr(msg)
		}
		if msg, err := h.validateLoan(ctx, userID, &s); err != nil || msg != "" {
			if err != nil {
				return err
			}
			return vErr(msg)
		}
		l, err = qtx.UpdateLoan(ctx, db.UpdateLoanParams{
			ID: id, UserID: userID, Name: s.name, Type: s.typ, Amount: s.amount, Currency: s.currency, Color: s.color,
			Icon: s.icon, Note: s.note, AccountID: pgUUID(s.accountID), OccurredAt: ts(&s.occurredAt),
		})
		if err != nil {
			return err
		}
		var existing *db.Transaction
		if t, err := qtx.GetLoanTransaction(ctx, pu(id)); err == nil {
			existing = &t
		} else if !isNoRows(err) {
			return err
		}
		currencyChanged := s.currency != strings.TrimSpace(cur.Currency)
		_, msg, err := h.syncBooking(ctx, qtx, booking{
			userID: userID, loanID: id, accountID: s.accountID, typ: bookingType(s.typ, ""), amount: s.amount,
			currency: s.currency, accountAmount: optInt64(req.AccountAmount), keepAmount: !currencyChanged && s.amount == cur.Amount,
			title: loanBookingTitle(l), note: s.note, occurredAt: s.occurredAt,
		}, existing)
		if err != nil {
			return err
		}
		if msg != "" {
			return vErr(msg)
		}
		// Records' bookings follow the loan's type, name and currency.
		records, err := qtx.ListLoanRecords(ctx, pu(id))
		if err != nil {
			return err
		}
		for _, rec := range records {
			if !rec.AccountID.Valid {
				continue
			}
			_, msg, err := h.saveRecordBooking(ctx, qtx, l, rec, nil, !currencyChanged)
			if err != nil {
				return err
			}
			if msg != "" {
				return vErr("record " + rec.ID.String() + ": " + msg)
			}
		}
		return nil
	})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	h.writeLoan(w, r, http.StatusOK, l)
}

// DeleteLoan soft-deletes the loan, its records and every booking transaction.
func (h *Handler) DeleteLoan(w http.ResponseWriter, r *http.Request) {
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
	n, err := qtx.SoftDeleteLoan(ctx, db.SoftDeleteLoanParams{ID: id, UserID: userID})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if n == 0 {
		httpx.WriteError(w, r, httpx.ErrNotFound)
		return
	}
	if err := qtx.SoftDeleteLoanRecordsByLoan(ctx, pu(id)); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if err := qtx.SoftDeleteLoanTransactions(ctx, pu(id)); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if err := tx.Commit(ctx); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"message": "deleted"})
}

func (h *Handler) ReorderLoans(w http.ResponseWriter, r *http.Request) {
	userID := currentUser(r)
	var req struct {
		IDs []uuid.UUID `json:"ids"`
	}
	if !decode(w, r, &req) {
		return
	}
	if len(req.IDs) == 0 || len(uniqueUUIDs(req.IDs)) != len(req.IDs) {
		validation(w, "ids must be a non-empty list of distinct loan ids")
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
		n, err := qtx.SetLoanSortOrder(ctx, db.SetLoanSortOrderParams{ID: id, UserID: userID, SortOrder: int32(i)})
		if err != nil {
			httpx.WriteError(w, r, err)
			return
		}
		if n == 0 {
			validation(w, "unknown loan id "+id.String())
			return
		}
	}
	if err := tx.Commit(ctx); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	h.ListLoans(w, r)
}

// ---------------------------------------------------------------- records

func (h *Handler) ListLoanRecords(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	if _, err := h.q().GetLoan(ctx, db.GetLoanParams{ID: id, UserID: currentUser(r)}); err != nil {
		httpx.WriteError(w, r, httpx.ErrNotFound)
		return
	}
	rows, err := h.q().ListLoanRecords(ctx, pu(id))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	out := make([]map[string]any, 0, len(rows))
	for _, rec := range rows {
		out = append(out, loanRecordJSON(rec))
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"data": out})
}

type loanRecordReq struct {
	Type          Opt[string]    `json:"type"`
	Amount        Opt[int64]     `json:"amount"`
	Note          Opt[string]    `json:"note"`
	OccurredAt    Opt[string]    `json:"occurred_at"`
	AccountID     Opt[uuid.UUID] `json:"account_id"`
	AccountAmount Opt[int64]     `json:"account_amount"`
}

type recordState struct {
	typ, note  string
	amount     int64
	occurredAt time.Time
	accountID  *uuid.UUID
}

func (s *recordState) apply(req loanRecordReq) string {
	if v, ok := req.Type.Value(); ok {
		s.typ = strings.ToUpper(strings.TrimSpace(v))
	}
	if v, ok := req.Amount.Value(); ok {
		s.amount = v
	}
	if v, ok := req.Note.Value(); ok {
		s.note = v
	}
	if v, ok := req.OccurredAt.Value(); ok {
		t, msg := optTime(&v)
		if msg != "" {
			return "occurred_at: " + msg
		}
		if t != nil {
			s.occurredAt = *t
		}
	}
	if req.AccountID.Set {
		s.accountID = nil
		if v, ok := req.AccountID.Value(); ok {
			s.accountID = &v
		}
	}
	if s.typ != "INCREASE" && s.typ != "DECREASE" {
		return "type must be INCREASE or DECREASE"
	}
	if s.amount <= 0 || s.amount > maxAmount {
		return "amount must be > 0 (minor units)"
	}
	if len(s.note) > maxDescriptionLen {
		return "note too long (max 2000)"
	}
	return ""
}

// saveRecordBooking syncs a record's booking and stores the link on the record.
func (h *Handler) saveRecordBooking(ctx context.Context, q *db.Queries, l db.Loan, rec db.LoanRecord, aa *int64, keep bool) (db.LoanRecord, string, error) {
	var ex *db.Transaction
	if rec.TransactionID.Valid {
		if t, err := q.GetTransaction(ctx, db.GetTransactionParams{ID: uuid.UUID(rec.TransactionID.Bytes), UserID: rec.UserID}); err == nil {
			ex = &t
		} else if !isNoRows(err) {
			return rec, "", err
		}
	}
	recID := rec.ID
	txID, msg, err := h.syncBooking(ctx, q, booking{
		userID: rec.UserID, loanID: l.ID, recordID: &recID, accountID: uuidPtr(rec.AccountID), typ: bookingType(l.Type, rec.Type),
		amount: rec.Amount, currency: strings.TrimSpace(l.Currency), accountAmount: aa, keepAmount: keep,
		title: loanBookingTitle(l), note: rec.Note, occurredAt: rec.OccurredAt.Time,
	}, ex)
	if err != nil || msg != "" {
		return rec, msg, err
	}
	if err := q.SetLoanRecordTransaction(ctx, db.SetLoanRecordTransactionParams{ID: rec.ID, TransactionID: pgUUID(txID)}); err != nil {
		return rec, "", err
	}
	rec.TransactionID = pgUUID(txID)
	return rec, "", nil
}

func optInt64(o Opt[int64]) *int64 {
	if v, ok := o.Value(); ok {
		return &v
	}
	return nil
}

func (h *Handler) CreateLoanRecord(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	userID := currentUser(r)
	ctx := r.Context()
	if _, err := h.q().GetLoan(ctx, db.GetLoanParams{ID: id, UserID: userID}); err != nil {
		httpx.WriteError(w, r, httpx.ErrNotFound)
		return
	}
	var req loanRecordReq
	if !decode(w, r, &req) {
		return
	}
	s := recordState{occurredAt: time.Now().UTC()}
	if msg := s.apply(req); msg != "" {
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
	l, err := qtx.LockLoan(ctx, db.LockLoanParams{ID: id, UserID: userID})
	if err != nil {
		httpx.WriteError(w, r, httpx.ErrNotFound)
		return
	}
	rec, err := qtx.CreateLoanRecord(ctx, db.CreateLoanRecordParams{
		LoanID: pu(id), UserID: userID, Type: s.typ, Amount: s.amount, Note: s.note,
		OccurredAt: ts(&s.occurredAt), AccountID: pgUUID(s.accountID),
	})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	rec, msg, err := h.saveRecordBooking(ctx, qtx, l, rec, optInt64(req.AccountAmount), false)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if msg != "" {
		validation(w, msg)
		return
	}
	if err := tx.Commit(ctx); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, loanRecordJSON(rec))
}

// UpdateLoanRecord applies a partial update merged onto the locked record (loan
// row locked first, as every loan write does) and keeps its booking in sync.
func (h *Handler) UpdateLoanRecord(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	userID := currentUser(r)
	ctx := r.Context()
	first, err := h.q().GetLoanRecord(ctx, db.GetLoanRecordParams{ID: id, UserID: userID})
	if err != nil {
		httpx.WriteError(w, r, httpx.ErrNotFound)
		return
	}
	var req loanRecordReq
	if !decode(w, r, &req) {
		return
	}
	var rec db.LoanRecord
	err = h.withTx(ctx, func(qtx *db.Queries) error {
		l, err := qtx.LockLoan(ctx, db.LockLoanParams{ID: uuid.UUID(first.LoanID.Bytes), UserID: userID})
		if isNoRows(err) {
			return httpx.ErrNotFound
		}
		if err != nil {
			return err
		}
		cur, err := qtx.LockLoanRecord(ctx, db.LockLoanRecordParams{ID: id, UserID: userID})
		if isNoRows(err) {
			return httpx.ErrNotFound
		}
		if err != nil {
			return err
		}
		s := recordState{typ: cur.Type, note: cur.Note, amount: cur.Amount, occurredAt: cur.OccurredAt.Time.UTC(), accountID: uuidPtr(cur.AccountID)}
		if msg := s.apply(req); msg != "" {
			return vErr(msg)
		}
		rec, err = qtx.UpdateLoanRecord(ctx, db.UpdateLoanRecordParams{
			ID: id, UserID: userID, Type: s.typ, Amount: s.amount, Note: s.note,
			OccurredAt: ts(&s.occurredAt), AccountID: pgUUID(s.accountID),
		})
		if err != nil {
			return err
		}
		var msg string
		rec, msg, err = h.saveRecordBooking(ctx, qtx, l, rec, optInt64(req.AccountAmount), s.amount == cur.Amount)
		if err != nil {
			return err
		}
		if msg != "" {
			return vErr(msg)
		}
		return nil
	})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, loanRecordJSON(rec))
}

func (h *Handler) DeleteLoanRecord(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	userID := currentUser(r)
	ctx := r.Context()
	cur, err := h.q().GetLoanRecord(ctx, db.GetLoanRecordParams{ID: id, UserID: userID})
	if err != nil {
		httpx.WriteError(w, r, httpx.ErrNotFound)
		return
	}
	tx, err := h.Pool.Begin(ctx)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := h.q().WithTx(tx)
	n, err := qtx.SoftDeleteLoanRecord(ctx, db.SoftDeleteLoanRecordParams{ID: id, UserID: userID})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if n == 0 {
		httpx.WriteError(w, r, httpx.ErrNotFound)
		return
	}
	if cur.TransactionID.Valid {
		if err := qtx.SoftDeleteTransactionByID(ctx, uuid.UUID(cur.TransactionID.Bytes)); err != nil {
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
