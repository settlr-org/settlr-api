package wallet

import (
	"context"
	"errors"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	db "github.com/settlr-org/settlr-api/internal/db"
	"github.com/settlr-org/settlr-api/internal/httpx"
)

// Planned payments (Ivy "planned payments"): a plan is a one-time or recurring
// INCOME/EXPENSE/TRANSFER. The server keeps exactly one pending transaction
// (occurred_at NULL, due_at set, planned_payment_id set) per active plan. Paying
// it sets occurred_at (it then counts in balances) and generates the next
// occurrence; skipping deletes it and generates the next. resolved_until is the
// due date of the latest paid/skipped occurrence; the next one is the first
// occurrence after it, computed from start_at (month-end clamped).

const (
	codeLoanManaged = "LOAN_MANAGED"
	codeNotPlanned  = "NOT_PLANNED"
)

var (
	errLoanManaged = &httpx.AppError{Code: codeLoanManaged, Message: "this transaction belongs to a loan; change it through the loan endpoints", Status: http.StatusConflict}
	errNotPlanned  = &httpx.AppError{Code: codeNotPlanned, Message: "transaction is already paid", Status: http.StatusConflict}
)

func vErr(msg string) error {
	return &httpx.AppError{Code: "VALIDATION_ERROR", Message: msg, Status: http.StatusUnprocessableEntity}
}

func isRetryable(err error) bool {
	var pe *pgconn.PgError
	return errors.As(err, &pe) && (pe.Code == "40P01" || pe.Code == "40001")
}

// withTx runs fn in a database transaction and commits it. A deadlock or
// serialization failure is retried once (fn must only assign its results).
func (h *Handler) withTx(ctx context.Context, fn func(q *db.Queries) error) error {
	for attempt := 0; ; attempt++ {
		tx, err := h.Pool.Begin(ctx)
		if err != nil {
			return err
		}
		err = fn(h.q().WithTx(tx))
		if err == nil {
			err = tx.Commit(ctx)
		}
		if err == nil {
			return nil
		}
		_ = tx.Rollback(ctx)
		if attempt == 0 && isRetryable(err) {
			continue
		}
		return err
	}
}

// lockTxnPlanFirst locks a transaction for pay/skip/edit/delete. Every path takes
// the plan's row lock before the transaction's (syncPending runs under the plan
// lock and then touches the pending row), so the two orders can never deadlock.
func lockTxnPlanFirst(ctx context.Context, q *db.Queries, id, userID uuid.UUID) (db.Transaction, error) {
	t, err := q.GetTransaction(ctx, db.GetTransactionParams{ID: id, UserID: userID})
	if isNoRows(err) {
		return t, httpx.ErrNotFound
	}
	if err != nil {
		return t, err
	}
	if t.PlannedPaymentID.Valid {
		if _, err := q.LockPlannedPayment(ctx, db.LockPlannedPaymentParams{ID: uuid.UUID(t.PlannedPaymentID.Bytes), UserID: userID}); err != nil && !isNoRows(err) {
			return t, err
		}
	}
	t, err = q.LockTransaction(ctx, db.LockTransactionParams{ID: id, UserID: userID})
	if isNoRows(err) {
		return t, httpx.ErrNotFound
	}
	return t, err
}

func loanManaged(w http.ResponseWriter) {
	writeErr(w, http.StatusConflict, codeLoanManaged, "this transaction belongs to a loan; change it through the loan endpoints")
}

// decodeOptional reads an optional JSON body: an empty body is fine.
func decodeOptional(w http.ResponseWriter, r *http.Request, dst any) bool {
	if err := httpx.DecodeJSON(r, dst); err != nil && !errors.Is(err, io.EOF) {
		writeErr(w, http.StatusBadRequest, "VALIDATION_ERROR", err.Error())
		return false
	}
	return true
}

func planJSON(p db.PlannedPayment, tags []tagRef, next map[string]any) map[string]any {
	if tags == nil {
		tags = []tagRef{}
	}
	var unit *string
	if p.IntervalUnit.Valid {
		unit = &p.IntervalUnit.String
	}
	var n *int32
	if p.IntervalN.Valid {
		n = &p.IntervalN.Int32
	}
	var nextDue any
	if next != nil {
		nextDue = next["due_at"]
	}
	return map[string]any{
		"id":            p.ID,
		"type":          p.Type,
		"account_id":    uuidPtr(p.AccountID),
		"to_account_id": uuidPtr(p.ToAccountID),
		"amount":        p.Amount,
		"currency":      strings.TrimSpace(p.Currency),
		"to_amount":     int8Ptr(p.ToAmount),
		"category_id":   nilIfZero(p.CategoryID),
		"title":         p.Title,
		"description":   p.Description,
		"start_at":      p.StartAt.Time.UTC(),
		"one_time":      p.OneTime,
		"interval_n":    n,
		"interval_unit": unit,
		"active":        p.Active,
		"tags":          tags,
		"next":          next,
		"next_due_at":   nextDue,
		"created_at":    p.CreatedAt.Time.UTC(),
		"updated_at":    p.UpdatedAt.Time.UTC(),
	}
}

// planRef is the plan summary attached to planned items in overview/summaries.
func planRef(p db.PlannedPayment) map[string]any {
	var unit *string
	if p.IntervalUnit.Valid {
		unit = &p.IntervalUnit.String
	}
	var n *int32
	if p.IntervalN.Valid {
		n = &p.IntervalN.Int32
	}
	return map[string]any{
		"id": p.ID, "title": p.Title, "one_time": p.OneTime, "interval_n": n, "interval_unit": unit,
		"start_at": p.StartAt.Time.UTC(), "active": p.Active,
	}
}

func (h *Handler) planTags(ctx context.Context, q *db.Queries, ids []uuid.UUID) (map[uuid.UUID][]tagRef, error) {
	out := map[uuid.UUID][]tagRef{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := q.ListPlannedPaymentTags(ctx, pgUUIDs(ids))
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		pid := uuid.UUID(row.PlannedPaymentID.Bytes)
		out[pid] = append(out[pid], tagRef{ID: row.ID, Name: row.Name, Color: row.Color, Icon: row.Icon})
	}
	return out, nil
}

// nextDue is the due date of the pending occurrence the plan should have, or nil.
func nextDue(p db.PlannedPayment) *time.Time {
	if !p.Active || p.DeletedAt.Valid {
		return nil
	}
	start := p.StartAt.Time.UTC()
	if !p.ResolvedUntil.Valid {
		return &start
	}
	if p.OneTime {
		return nil
	}
	n := 1
	if p.IntervalN.Valid {
		n = int(p.IntervalN.Int32)
	}
	t := nextOccurrenceAfter(start, n, p.IntervalUnit.String, p.ResolvedUntil.Time)
	return &t
}

// syncPending makes the plan's pending transaction match the plan: created,
// updated in place (same id) or deleted. It returns the pending item, if any.
func (h *Handler) syncPending(ctx context.Context, q *db.Queries, p db.PlannedPayment) (*db.Transaction, error) {
	due := nextDue(p)
	pending, err := q.GetPendingForPlan(ctx, pu(p.ID))
	has := err == nil
	if err != nil && !isNoRows(err) {
		return nil, err
	}
	if due == nil {
		if has {
			return nil, q.SoftDeleteTransactionByID(ctx, pending.ID)
		}
		return nil, nil
	}
	var cat *uuid.UUID
	if p.CategoryID != uuid.Nil {
		c := p.CategoryID
		cat = &c
	}
	var t db.Transaction
	if has {
		t, err = q.UpdateTransaction(ctx, db.UpdateTransactionParams{
			ID: pending.ID, UserID: p.UserID, Type: p.Type, AccountID: p.AccountID, ToAccountID: p.ToAccountID,
			Amount: p.Amount, Currency: strings.TrimSpace(p.Currency), ToAmount: p.ToAmount, CategoryID: pgUUID(cat),
			Title: p.Title, Description: p.Description, DueAt: ts(due),
		})
	} else {
		t, err = q.CreateTransaction(ctx, db.CreateTransactionParams{
			ID: uuid.New(), UserID: p.UserID, Type: p.Type, AccountID: p.AccountID, ToAccountID: p.ToAccountID,
			Amount: p.Amount, Currency: strings.TrimSpace(p.Currency), ToAmount: p.ToAmount, CategoryID: pgUUID(cat),
			Title: p.Title, Description: p.Description, DueAt: ts(due), PlannedPaymentID: pu(p.ID), Source: "MANUAL",
		})
	}
	if err != nil {
		return nil, err
	}
	tags, err := h.planTags(ctx, q, []uuid.UUID{p.ID})
	if err != nil {
		return nil, err
	}
	if err := q.DeleteTransactionTags(ctx, pu(t.ID)); err != nil {
		return nil, err
	}
	if ids := tagIDsOf(tags[p.ID]); len(ids) > 0 {
		if err := q.AddTransactionTags(ctx, db.AddTransactionTagsParams{TransactionID: pu(t.ID), TagIds: pgUUIDs(ids)}); err != nil {
			return nil, err
		}
	}
	return &t, nil
}

func tagIDsOf(tags []tagRef) []uuid.UUID {
	out := make([]uuid.UUID, 0, len(tags))
	for _, t := range tags {
		out = append(out, t.ID)
	}
	return out
}

// advancePlan records that the occurrence due at `due` was paid or skipped and
// generates the next pending occurrence. With skipOverdue, every occurrence due
// before now is resolved too. A one-time plan becomes inactive. q must be bound
// to a transaction (the plan row is locked).
func (h *Handler) advancePlan(ctx context.Context, q *db.Queries, planID, userID uuid.UUID, due time.Time, skipOverdue bool) (*db.Transaction, error) {
	p, err := q.LockPlannedPayment(ctx, db.LockPlannedPaymentParams{ID: planID, UserID: userID})
	if isNoRows(err) {
		return nil, nil // plan deleted: the occurrence stays as history
	}
	if err != nil {
		return nil, err
	}
	resolved := due.UTC()
	if p.ResolvedUntil.Valid && p.ResolvedUntil.Time.After(resolved) {
		resolved = p.ResolvedUntil.Time.UTC()
	}
	if skipOverdue && !p.OneTime {
		n := 1
		if p.IntervalN.Valid {
			n = int(p.IntervalN.Int32)
		}
		now := time.Now().UTC()
		for {
			next := nextOccurrenceAfter(p.StartAt.Time, n, p.IntervalUnit.String, resolved)
			if !next.Before(now) {
				break
			}
			resolved = next
		}
	}
	active := p.Active && !p.OneTime
	p, err = q.UpdatePlannedPayment(ctx, planUpdateParams(p, active, pgtype.Timestamptz{Time: resolved, Valid: true}))
	if err != nil {
		return nil, err
	}
	return h.syncPending(ctx, q, p)
}

func planUpdateParams(p db.PlannedPayment, active bool, resolved pgtype.Timestamptz) db.UpdatePlannedPaymentParams {
	var cat *uuid.UUID
	if p.CategoryID != uuid.Nil {
		c := p.CategoryID
		cat = &c
	}
	return db.UpdatePlannedPaymentParams{
		ID: p.ID, UserID: p.UserID, Type: p.Type, AccountID: p.AccountID, ToAccountID: p.ToAccountID,
		Amount: p.Amount, Currency: strings.TrimSpace(p.Currency), ToAmount: p.ToAmount, CategoryID: pgUUID(cat),
		Title: p.Title, Description: p.Description, StartAt: p.StartAt, OneTime: p.OneTime,
		IntervalN: p.IntervalN, IntervalUnit: p.IntervalUnit, Active: active, ResolvedUntil: resolved,
	}
}

// ---------------------------------------------------------------- plan CRUD

type planReq struct {
	Type         Opt[string]      `json:"type"`
	AccountID    Opt[uuid.UUID]   `json:"account_id"`
	ToAccountID  Opt[uuid.UUID]   `json:"to_account_id"`
	Amount       Opt[int64]       `json:"amount"`
	ToAmount     Opt[int64]       `json:"to_amount"`
	Currency     Opt[string]      `json:"currency"`
	CategoryID   Opt[uuid.UUID]   `json:"category_id"`
	Title        Opt[string]      `json:"title"`
	Description  Opt[string]      `json:"description"`
	StartAt      Opt[string]      `json:"start_at"`
	OneTime      Opt[bool]        `json:"one_time"`
	IntervalN    Opt[int32]       `json:"interval_n"`
	IntervalUnit Opt[string]      `json:"interval_unit"`
	Active       Opt[bool]        `json:"active"`
	TagIDs       Opt[[]uuid.UUID] `json:"tag_ids"`
}

// planState is the full, merged state of a plan to validate and store.
type planState struct {
	txn          txnInput
	oneTime      bool
	intervalN    int32
	intervalUnit string
	active       bool
}

// apply merges a request into the state (create starts from the zero state).
func (s *planState) apply(req planReq) string {
	in := &s.txn
	if v, ok := req.Type.Value(); ok {
		in.Type = v
		if strings.ToUpper(strings.TrimSpace(v)) != "TRANSFER" {
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
		in.Currency = ""
		if !req.ToAmount.Set && in.Type == "TRANSFER" {
			in.ToAmount = nil
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
		if !req.ToAmount.Set && in.ToAmount != nil && *in.ToAmount == in.Amount {
			in.ToAmount = nil // same-currency transfer: follow the new amount
		}
		in.Amount = v
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
	if req.StartAt.Set {
		v, _ := req.StartAt.Value()
		t, msg := optTime(&v)
		if msg != "" {
			return "start_at: " + msg
		}
		in.DueAt = t
	}
	if v, ok := req.OneTime.Value(); ok {
		s.oneTime = v
	}
	if req.IntervalN.Set {
		s.intervalN = 0
		if v, ok := req.IntervalN.Value(); ok {
			s.intervalN = v
		}
	}
	if req.IntervalUnit.Set {
		s.intervalUnit = ""
		if v, ok := req.IntervalUnit.Value(); ok {
			s.intervalUnit = strings.ToUpper(strings.TrimSpace(v))
		}
	}
	if v, ok := req.Active.Value(); ok {
		s.active = v
	}
	if req.TagIDs.Set {
		in.TagIDs = []uuid.UUID{}
		if v, ok := req.TagIDs.Value(); ok && v != nil {
			in.TagIDs = v
		}
	}
	return ""
}

func (h *Handler) validatePlan(ctx context.Context, userID uuid.UUID, s *planState) (string, error) {
	if s.txn.DueAt == nil {
		return "start_at required", nil
	}
	s.txn.OccurredAt = nil
	if s.oneTime {
		s.intervalN, s.intervalUnit = 0, ""
	} else {
		if s.intervalN == 0 {
			s.intervalN = 1
		}
		if s.intervalN < 1 || s.intervalN > 1000 {
			return "interval_n must be between 1 and 1000", nil
		}
		if !intervalUnits[s.intervalUnit] {
			return "interval_unit must be DAY, WEEK, MONTH or YEAR (or set one_time)", nil
		}
	}
	return h.validate(ctx, userID, &s.txn)
}

func (s *planState) interval() (pgtype.Int4, pgtype.Text) {
	if s.oneTime {
		return pgtype.Int4{}, pgtype.Text{}
	}
	return pgtype.Int4{Int32: s.intervalN, Valid: true}, pgtype.Text{String: s.intervalUnit, Valid: true}
}

func (h *Handler) writePlan(w http.ResponseWriter, r *http.Request, status int, p db.PlannedPayment) {
	out, err := h.plansJSON(r.Context(), []db.PlannedPayment{p})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, status, out[0])
}

// plansJSON renders plans with their tags and pending occurrence.
func (h *Handler) plansJSON(ctx context.Context, plans []db.PlannedPayment) ([]map[string]any, error) {
	ids := make([]uuid.UUID, len(plans))
	for i, p := range plans {
		ids[i] = p.ID
	}
	tags, err := h.planTags(ctx, h.q(), ids)
	if err != nil {
		return nil, err
	}
	pending, err := h.q().ListPendingForPlans(ctx, pgUUIDs(ids))
	if err != nil {
		return nil, err
	}
	txIDs := make([]uuid.UUID, len(pending))
	byPlan := map[uuid.UUID]db.Transaction{}
	for i, t := range pending {
		txIDs[i] = t.ID
		byPlan[uuid.UUID(t.PlannedPaymentID.Bytes)] = t
	}
	txTags, err := h.tagsFor(ctx, txIDs)
	if err != nil {
		return nil, err
	}
	out := make([]map[string]any, 0, len(plans))
	for _, p := range plans {
		var next map[string]any
		if t, ok := byPlan[p.ID]; ok {
			next = transactionJSON(t, txTags[t.ID])
		}
		out = append(out, planJSON(p, tags[p.ID], next))
	}
	return out, nil
}

// ListPlanned lists the user's plans (filters: type, active=true|false), active
// ones first, by next due date.
func (h *Handler) ListPlanned(w http.ResponseWriter, r *http.Request) {
	userID := currentUser(r)
	var active pgtype.Bool
	switch strings.ToLower(strings.TrimSpace(r.URL.Query().Get("active"))) {
	case "", "all":
	case "true", "1":
		active = pgtype.Bool{Bool: true, Valid: true}
	case "false", "0":
		active = pgtype.Bool{Bool: false, Valid: true}
	default:
		validation(w, "active must be true, false or all")
		return
	}
	plans, err := h.q().ListPlannedPayments(r.Context(), db.ListPlannedPaymentsParams{UserID: userID, Types: queryList(r, "type"), Active: active})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	out, err := h.plansJSON(r.Context(), plans)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	sort.SliceStable(out, func(i, j int) bool {
		ai, aj := out[i]["active"].(bool), out[j]["active"].(bool)
		if ai != aj {
			return ai
		}
		di, _ := out[i]["next_due_at"].(*time.Time)
		dj, _ := out[j]["next_due_at"].(*time.Time)
		switch {
		case di == nil || dj == nil:
			return di != nil && dj == nil
		default:
			return di.Before(*dj)
		}
	})
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"data": out})
}

func (h *Handler) GetPlanned(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	p, err := h.q().GetPlannedPayment(r.Context(), db.GetPlannedPaymentParams{ID: id, UserID: currentUser(r)})
	if err != nil {
		httpx.WriteError(w, r, httpx.ErrNotFound)
		return
	}
	h.writePlan(w, r, http.StatusOK, p)
}

func (h *Handler) CreatePlanned(w http.ResponseWriter, r *http.Request) {
	userID := currentUser(r)
	ctx := r.Context()
	var req planReq
	if !decode(w, r, &req) {
		return
	}
	s := planState{active: true, txn: txnInput{TagIDs: []uuid.UUID{}}}
	if msg := s.apply(req); msg != "" {
		validation(w, msg)
		return
	}
	msg, err := h.validatePlan(ctx, userID, &s)
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
	in := s.txn
	n, unit := s.interval()
	p, err := qtx.CreatePlannedPayment(ctx, db.CreatePlannedPaymentParams{
		UserID: userID, Type: in.Type, AccountID: pgUUID(in.AccountID), ToAccountID: pgUUID(in.ToAccountID),
		Amount: in.Amount, Currency: in.Currency, ToAmount: pgInt8(in.ToAmount), CategoryID: pgUUID(in.CategoryID),
		Title: in.Title, Description: in.Description, StartAt: ts(in.DueAt), OneTime: s.oneTime,
		IntervalN: n, IntervalUnit: unit, Active: s.active,
	})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if len(in.TagIDs) > 0 {
		if err := qtx.AddPlannedPaymentTags(ctx, db.AddPlannedPaymentTagsParams{PlannedPaymentID: pu(p.ID), TagIds: pgUUIDs(in.TagIDs)}); err != nil {
			httpx.WriteError(w, r, err)
			return
		}
	}
	if _, err := h.syncPending(ctx, qtx, p); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if err := tx.Commit(ctx); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	h.writePlan(w, r, http.StatusCreated, p)
}

// UpdatePlanned applies a partial update and regenerates the pending occurrence:
// it is updated in place (same id) to the new amount/fields and the next due date
// per the (possibly new) schedule. Re-activating a completed one-time plan
// re-opens it at start_at. The update is merged onto the locked row, so a
// concurrent pay/skip is never undone.
func (h *Handler) UpdatePlanned(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	userID := currentUser(r)
	ctx := r.Context()
	if _, err := h.q().GetPlannedPayment(ctx, db.GetPlannedPaymentParams{ID: id, UserID: userID}); err != nil {
		httpx.WriteError(w, r, httpx.ErrNotFound)
		return
	}
	var req planReq
	if !decode(w, r, &req) {
		return
	}
	var p db.PlannedPayment
	err := h.withTx(ctx, func(qtx *db.Queries) error {
		cur, err := qtx.LockPlannedPayment(ctx, db.LockPlannedPaymentParams{ID: id, UserID: userID})
		if isNoRows(err) {
			return httpx.ErrNotFound
		}
		if err != nil {
			return err
		}
		start := cur.StartAt.Time.UTC()
		s := planState{
			txn: txnInput{
				Type: cur.Type, AccountID: uuidPtr(cur.AccountID), ToAccountID: uuidPtr(cur.ToAccountID), Amount: cur.Amount,
				ToAmount: int8Ptr(cur.ToAmount), CategoryID: nilIfZero(cur.CategoryID), Title: cur.Title,
				Description: cur.Description, DueAt: &start,
			},
			oneTime: cur.OneTime, intervalN: cur.IntervalN.Int32, intervalUnit: cur.IntervalUnit.String, active: cur.Active,
		}
		if s.txn.AccountID == nil {
			s.txn.Currency = cur.Currency
		}
		if msg := s.apply(req); msg != "" {
			return vErr(msg)
		}
		if msg, err := h.validatePlan(ctx, userID, &s); err != nil || msg != "" {
			if err != nil {
				return err
			}
			return vErr(msg)
		}
		resolved := cur.ResolvedUntil
		if s.active && !cur.Active && s.oneTime {
			resolved = pgtype.Timestamptz{} // re-open a completed one-time plan
		}
		in := s.txn
		n, unit := s.interval()
		p, err = qtx.UpdatePlannedPayment(ctx, db.UpdatePlannedPaymentParams{
			ID: id, UserID: userID, Type: in.Type, AccountID: pgUUID(in.AccountID), ToAccountID: pgUUID(in.ToAccountID),
			Amount: in.Amount, Currency: in.Currency, ToAmount: pgInt8(in.ToAmount), CategoryID: pgUUID(in.CategoryID),
			Title: in.Title, Description: in.Description, StartAt: ts(in.DueAt), OneTime: s.oneTime,
			IntervalN: n, IntervalUnit: unit, Active: s.active, ResolvedUntil: resolved,
		})
		if err != nil {
			return err
		}
		if in.TagIDs != nil {
			if err := qtx.DeletePlannedPaymentTags(ctx, pu(id)); err != nil {
				return err
			}
			if len(in.TagIDs) > 0 {
				if err := qtx.AddPlannedPaymentTags(ctx, db.AddPlannedPaymentTagsParams{PlannedPaymentID: pu(id), TagIds: pgUUIDs(in.TagIDs)}); err != nil {
					return err
				}
			}
		}
		_, err = h.syncPending(ctx, qtx, p)
		return err
	})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	h.writePlan(w, r, http.StatusOK, p)
}

// DeletePlanned deletes the plan and its pending occurrence; paid occurrences
// stay (they keep planned_payment_id as history).
func (h *Handler) DeletePlanned(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	userID := currentUser(r)
	ctx := r.Context()
	err := h.withTx(ctx, func(qtx *db.Queries) error {
		// Plan row first (the UPDATE locks it), then its pending occurrence.
		n, err := qtx.SoftDeletePlannedPayment(ctx, db.SoftDeletePlannedPaymentParams{ID: id, UserID: userID})
		if err != nil {
			return err
		}
		if n == 0 {
			return httpx.ErrNotFound
		}
		pending, err := qtx.GetPendingForPlan(ctx, pu(id))
		if isNoRows(err) {
			return nil
		}
		if err != nil {
			return err
		}
		_, err = qtx.SkipPendingTransaction(ctx, pending.ID)
		return err
	})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"message": "deleted"})
}

// ---------------------------------------------------------------- pay / skip

type payReq struct {
	OccurredAt *string    `json:"occurred_at"`
	Amount     *int64     `json:"amount"`
	ToAmount   *int64     `json:"to_amount"`
	AccountID  *uuid.UUID `json:"account_id"`
}

// PayTransaction marks a planned (unpaid) transaction as paid: occurred_at is set
// (default now) so it counts in balances, optionally with another amount or
// account. For a plan's occurrence the next one is generated.
func (h *Handler) PayTransaction(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	userID := currentUser(r)
	ctx := r.Context()
	var req payReq
	if !decodeOptional(w, r, &req) {
		return
	}
	occurred := time.Now().UTC()
	if req.OccurredAt != nil {
		t, msg := optTime(req.OccurredAt)
		if msg != "" {
			validation(w, "occurred_at: "+msg)
			return
		}
		if t != nil {
			occurred = *t
		}
	}
	var paid db.Transaction
	var next *db.Transaction
	err := h.withTx(ctx, func(qtx *db.Queries) error {
		cur, err := lockTxnPlanFirst(ctx, qtx, id, userID)
		if err != nil {
			return err
		}
		if cur.Source == "LOAN" {
			return errLoanManaged
		}
		if cur.OccurredAt.Valid {
			return errNotPlanned
		}
		in := txnInput{
			Type: cur.Type, AccountID: uuidPtr(cur.AccountID), ToAccountID: uuidPtr(cur.ToAccountID), Amount: cur.Amount,
			ToAmount: int8Ptr(cur.ToAmount), CategoryID: nilIfZero(cur.CategoryID), Title: cur.Title,
			Description: cur.Description, OccurredAt: &occurred, DueAt: tsPtr(cur.DueAt),
		}
		if in.AccountID == nil {
			in.Currency = cur.Currency
		}
		if req.AccountID != nil {
			in.AccountID, in.Currency = req.AccountID, ""
			if req.ToAmount == nil && in.Type == "TRANSFER" {
				in.ToAmount = nil
			}
		}
		if req.Amount != nil {
			if req.ToAmount == nil && in.ToAmount != nil && *in.ToAmount == in.Amount {
				in.ToAmount = nil
			}
			in.Amount = *req.Amount
		}
		if req.ToAmount != nil {
			in.ToAmount = req.ToAmount
		}
		if in.AccountID == nil {
			return vErr("account_id required to pay a planned item without account")
		}
		if msg, err := h.validate(ctx, userID, &in); err != nil || msg != "" {
			if err != nil {
				return err
			}
			return vErr(msg)
		}
		paid, err = qtx.UpdateTransaction(ctx, db.UpdateTransactionParams{
			ID: id, UserID: userID, Type: in.Type, AccountID: pgUUID(in.AccountID), ToAccountID: pgUUID(in.ToAccountID),
			Amount: in.Amount, Currency: in.Currency, ToAmount: pgInt8(in.ToAmount), CategoryID: pgUUID(in.CategoryID),
			Title: in.Title, Description: in.Description, OccurredAt: ts(in.OccurredAt), DueAt: ts(in.DueAt),
		})
		if err != nil {
			return err
		}
		next = nil
		if cur.PlannedPaymentID.Valid {
			next, err = h.advancePlan(ctx, qtx, uuid.UUID(cur.PlannedPaymentID.Bytes), userID, cur.DueAt.Time, false)
		}
		return err
	})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	h.writePayResult(w, r, &paid, next)
}

func (h *Handler) writePayResult(w http.ResponseWriter, r *http.Request, paid, next *db.Transaction) {
	var ids []uuid.UUID
	for _, t := range []*db.Transaction{paid, next} {
		if t != nil {
			ids = append(ids, t.ID)
		}
	}
	tags, err := h.tagsFor(r.Context(), ids)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	resp := map[string]any{"next": nil}
	if next != nil {
		resp["next"] = transactionJSON(*next, tags[next.ID])
	}
	if paid != nil {
		resp["transaction"] = transactionJSON(*paid, tags[paid.ID])
	}
	httpx.WriteJSON(w, http.StatusOK, resp)
}

// SkipTransaction deletes a planned occurrence without paying it; for a plan the
// next occurrence is generated (a one-time plan becomes inactive).
func (h *Handler) SkipTransaction(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	userID := currentUser(r)
	ctx := r.Context()
	var next *db.Transaction
	err := h.withTx(ctx, func(qtx *db.Queries) error {
		cur, err := lockTxnPlanFirst(ctx, qtx, id, userID)
		if err != nil {
			return err
		}
		if cur.Source == "LOAN" {
			return errLoanManaged
		}
		if cur.OccurredAt.Valid {
			return errNotPlanned
		}
		_, next, err = h.skipOne(ctx, qtx, cur, false)
		return err
	})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	h.writePayResult(w, r, nil, next)
}

func (h *Handler) skipOne(ctx context.Context, q *db.Queries, t db.Transaction, skipOverdue bool) (bool, *db.Transaction, error) {
	// The occurred_at guard is a backstop: callers hold the row lock and have
	// re-checked that it is still unpaid.
	n, err := q.SkipPendingTransaction(ctx, t.ID)
	if err != nil || n == 0 {
		return false, nil, err
	}
	if !t.PlannedPaymentID.Valid {
		return true, nil, nil
	}
	next, err := h.advancePlan(ctx, q, uuid.UUID(t.PlannedPaymentID.Bytes), t.UserID, t.DueAt.Time, skipOverdue)
	return true, next, err
}

// SkipOverdue skips every overdue planned item of the user; recurring plans move
// to their first occurrence due now or later. Each item is locked (plan first)
// and re-checked, so an occurrence paid concurrently is left alone.
func (h *Handler) SkipOverdue(w http.ResponseWriter, r *http.Request) {
	userID := currentUser(r)
	ctx := r.Context()
	now := time.Now().UTC()
	items, err := h.q().ListPlannedTransactions(ctx, db.ListPlannedTransactionsParams{UserID: userID, DueBefore: ts(&now)})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	// A stable lock order across requests: by plan, then by transaction.
	sort.Slice(items, func(i, j int) bool {
		pi, pj := uuid.UUID(items[i].PlannedPaymentID.Bytes).String(), uuid.UUID(items[j].PlannedPaymentID.Bytes).String()
		if pi != pj {
			return pi < pj
		}
		return items[i].ID.String() < items[j].ID.String()
	})
	skipped := 0
	err = h.withTx(ctx, func(qtx *db.Queries) error {
		skipped = 0
		for _, item := range items {
			t, err := lockTxnPlanFirst(ctx, qtx, item.ID, userID)
			if err == httpx.ErrNotFound {
				continue // deleted meanwhile
			}
			if err != nil {
				return err
			}
			if t.OccurredAt.Valid || t.Source == "LOAN" || !t.DueAt.Valid || !t.DueAt.Time.Before(now) {
				continue // paid or moved meanwhile
			}
			ok, _, err := h.skipOne(ctx, qtx, t, true)
			if err != nil {
				return err
			}
			if ok {
				skipped++
			}
		}
		return nil
	})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"skipped": skipped})
}

// ---------------------------------------------------------------- upcoming / overdue

type plannedView struct {
	upcoming, overdue             []map[string]any
	upIncome, upExpense           int64
	overdueIncome, overdueExpense int64
	projectedDelta                int64
}

// plannedItems loads unpaid planned items and splits them into overdue (due
// before now, any date) and upcoming (due now or later, within [from, to)).
// Amount sums are in the base currency. When accounts is given, projectedDelta
// is the signed base-currency effect of every item due before `to` on the
// accounts included in the balance (items without account count too).
func (h *Handler) plannedItems(ctx context.Context, userID uuid.UUID, f *fx, from, to *time.Time,
	accountID, categoryID *uuid.UUID, accounts []db.Account) (*plannedView, error) {
	now := time.Now().UTC()
	var dueBefore *time.Time
	if to != nil {
		b := *to
		if now.After(b) {
			b = now
		}
		dueBefore = &b
	}
	items, err := h.q().ListPlannedTransactions(ctx, db.ListPlannedTransactionsParams{
		UserID: userID, DueBefore: ts(dueBefore), AccountID: pgUUID(accountID), CategoryID: pgUUID(categoryID),
	})
	if err != nil {
		return nil, err
	}
	ids := make([]uuid.UUID, len(items))
	var planIDs []uuid.UUID
	for i, t := range items {
		ids[i] = t.ID
		if t.PlannedPaymentID.Valid {
			planIDs = append(planIDs, uuid.UUID(t.PlannedPaymentID.Bytes))
		}
	}
	tags, err := h.tagsFor(ctx, ids)
	if err != nil {
		return nil, err
	}
	plans := map[uuid.UUID]db.PlannedPayment{}
	if len(planIDs) > 0 {
		rows, err := h.q().ListPlannedPaymentsByIDs(ctx, pgUUIDs(uniqueUUIDs(planIDs)))
		if err != nil {
			return nil, err
		}
		for _, p := range rows {
			plans[p.ID] = p
		}
	}
	included := map[uuid.UUID]db.Account{}
	for _, a := range accounts {
		if a.IncludeInBalance {
			included[a.ID] = a
		}
	}
	v := &plannedView{upcoming: []map[string]any{}, overdue: []map[string]any{}}
	for _, t := range items {
		due := t.DueAt.Time
		j := transactionJSON(t, tags[t.ID])
		j["plan"] = nil
		if t.PlannedPaymentID.Valid {
			if p, ok := plans[uuid.UUID(t.PlannedPaymentID.Bytes)]; ok {
				j["plan"] = planRef(p)
			}
		}
		base, _ := f.convert(ctx, t.Amount, t.Currency, f.base)
		switch {
		case due.Before(now):
			v.overdue = append(v.overdue, j)
			if t.Type == "INCOME" {
				v.overdueIncome += base
			} else if t.Type == "EXPENSE" {
				v.overdueExpense += base
			}
		case (from == nil || !due.Before(*from)) && (to == nil || due.Before(*to)):
			v.upcoming = append(v.upcoming, j)
			if t.Type == "INCOME" {
				v.upIncome += base
			} else if t.Type == "EXPENSE" {
				v.upExpense += base
			}
		}
		if accounts == nil || (to != nil && !due.Before(*to)) {
			continue
		}
		counts := !t.AccountID.Valid // items without account count too
		if t.AccountID.Valid {
			_, counts = included[uuid.UUID(t.AccountID.Bytes)]
		}
		switch t.Type {
		case "INCOME":
			if counts {
				v.projectedDelta += base
			}
		case "EXPENSE":
			if counts {
				v.projectedDelta -= base
			}
		case "TRANSFER":
			if counts {
				v.projectedDelta -= base
			}
			if dst, ok := included[uuid.UUID(t.ToAccountID.Bytes)]; ok && t.ToAmount.Valid {
				if x, ok := f.convert(ctx, t.ToAmount.Int64, dst.Currency, f.base); ok {
					v.projectedDelta += x
				}
			}
		}
	}
	return v, nil
}
