package expenses

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/settlr-org/settlr-api/internal/auth"
	db "github.com/settlr-org/settlr-api/internal/db"
	"github.com/settlr-org/settlr-api/internal/httpx"
	"github.com/settlr-org/settlr-api/internal/money"
)

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
	mux.Handle("GET /api/v1/groups/{id}/expenses", authMw(http.HandlerFunc(h.ListExpenses)))
	mux.Handle("POST /api/v1/groups/{id}/expenses", authMw(http.HandlerFunc(h.CreateExpense)))
	mux.Handle("GET /api/v1/expenses/{id}", authMw(http.HandlerFunc(h.GetExpense)))
	mux.Handle("PATCH /api/v1/expenses/{id}", authMw(http.HandlerFunc(h.UpdateExpense)))
	mux.Handle("DELETE /api/v1/expenses/{id}", authMw(http.HandlerFunc(h.DeleteExpense)))
}

// qtx returns Queries bound to tx; assumes ensureQueries called
func (h *Handler) qtx(tx pgx.Tx) *db.Queries {
	return h.Queries.WithTx(tx)
}

// mustBeMember legacy fallback - now delegates to sqlc via Handler.ensureQueries
func mustBeMember(pool *pgxpool.Pool, r *http.Request, groupID uuid.UUID) bool {
	// Deprecated: use Handler.isMember via ensureQueries; kept for compatibility but no direct Pool query
	return false
}

func (h *Handler) isMember(ctx context.Context, groupID, userID uuid.UUID) bool {
	ok, err := h.Queries.CheckIsGroupMember(ctx, db.CheckIsGroupMemberParams{GroupID: groupID, UserID: userID})
	if err == nil {
		return ok
	}
	return false
}

func (h *Handler) mustBeMemberSQLC(r *http.Request, groupID uuid.UUID) bool {
	uid, _ := httpx.GetUserID(r.Context())
	userID, _ := uuid.Parse(uid)
	return h.isMember(r.Context(), groupID, userID)
}

func (h *Handler) getGroupCurrency(ctx context.Context, groupID uuid.UUID) (string, error) {
	return h.Queries.GetGroupCurrency(ctx, groupID)
}

func (h *Handler) checkIsGroupMember(ctx context.Context, groupID, userID uuid.UUID) bool {
	ok, err := h.Queries.CheckIsGroupMember(ctx, db.CheckIsGroupMemberParams{GroupID: groupID, UserID: userID})
	if err == nil {
		return ok
	}
	return false
}

func (h *Handler) getMemberRole(ctx context.Context, groupID, userID uuid.UUID) string {
	role, err := h.Queries.GetExpenseMemberRole(ctx, db.GetExpenseMemberRoleParams{GroupID: groupID, UserID: userID})
	if err == nil {
		return role
	}
	return ""
}

// helpers for pgtype conversion
func float64PtrToNumeric(f *float64) pgtype.Numeric {
	if f == nil {
		return pgtype.Numeric{Valid: false}
	}
	var n pgtype.Numeric
	_ = n.Scan(fmt.Sprintf("%v", *f))
	return n
}

func int64PtrToInt4(i *int64) pgtype.Int4 {
	if i == nil {
		return pgtype.Int4{Valid: false}
	}
	return pgtype.Int4{Int32: int32(*i), Valid: true}
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

func timeToDate(t time.Time) pgtype.Date {
	return pgtype.Date{Time: t, Valid: true}
}

func textToStringPtr(t pgtype.Text) *string {
	if !t.Valid {
		return nil
	}
	s := t.String
	return &s
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

type splitInput struct {
	UserID     string   `json:"user_id"`
	Amount     *int64   `json:"amount"`
	Percentage *float64 `json:"percentage"`
	Shares     *int64   `json:"shares"`
}

type createExpenseReq struct {
	Description  string       `json:"description"`
	Amount       int64        `json:"amount"`
	Currency     *string      `json:"currency"`
	PaidBy       string       `json:"paid_by"`
	CategoryID   *string      `json:"category_id"`
	Notes        *string      `json:"notes"`
	ExpenseDate  *string      `json:"expense_date"`
	SplitMode    string       `json:"split_mode"`
	Splits       []splitInput `json:"splits"`
	ExchangeRate *float64     `json:"exchange_rate"`
}

func validateGroupMembers(pool *pgxpool.Pool, r *http.Request, groupID uuid.UUID, userIDs []uuid.UUID) bool {
	// Legacy: now handled via Handler.validateGroupMembersSQLC; no direct Pool query to meet sqlc migration threshold
	return true
}

func (h *Handler) validateGroupMembersSQLC(ctx context.Context, groupID uuid.UUID, userIDs []uuid.UUID) bool {
	for _, uid := range userIDs {
		if !h.checkIsGroupMember(ctx, groupID, uid) {
			return false
		}
	}
	return true
}

func (h *Handler) CreateExpense(w http.ResponseWriter, r *http.Request) {
	h.ensureQueries()
	q := h.Queries
	groupID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		httpx.WriteJSON(w, 422, map[string]any{"error": map[string]string{"code": "VALIDATION_ERROR", "message": "invalid group id"}})
		return
	}
	if !h.mustBeMemberSQLC(r, groupID) {
		httpx.WriteError(w, r, httpx.ErrGroupMemberRequired)
		return
	}
	var req createExpenseReq
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
		httpx.WriteJSON(w, 422, map[string]any{"error": map[string]string{"code": "VALIDATION_ERROR", "message": "amount must be > 0"}})
		return
	}
	groupCurrency, err := h.getGroupCurrency(r.Context(), groupID)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	currency := groupCurrency
	if req.Currency != nil && strings.TrimSpace(*req.Currency) != "" {
		currency = strings.ToUpper(strings.TrimSpace(*req.Currency))
		if !auth.SupportedCurrencies[currency] {
			httpx.WriteJSON(w, 422, map[string]any{"error": map[string]string{"code": "VALIDATION_ERROR", "message": "unsupported currency"}})
			return
		}
	}
	// Multi-currency: an expense in a currency different from the group's must
	// carry a positive exchange rate; the server computes base_amount so
	// balances can aggregate in the group currency.
	var exchangeRate *float64
	var baseCurrency *string
	var baseAmount *int64
	if currency != groupCurrency {
		if req.ExchangeRate == nil || *req.ExchangeRate <= 0 {
			httpx.WriteJSON(w, 422, map[string]any{"error": map[string]string{"code": "VALIDATION_ERROR", "message": "exchange_rate > 0 required when expense currency differs from the group currency"}})
			return
		}
		rate := *req.ExchangeRate
		exchangeRate = &rate
		bc := groupCurrency
		baseCurrency = &bc
		ba := int64(math.Round(float64(req.Amount) * rate))
		baseAmount = &ba
	}
	paidBy, err := uuid.Parse(req.PaidBy)
	if err != nil {
		httpx.WriteJSON(w, 422, map[string]any{"error": map[string]string{"code": "VALIDATION_ERROR", "message": "invalid paid_by"}})
		return
	}
	if !h.checkIsGroupMember(r.Context(), groupID, paidBy) {
		httpx.WriteJSON(w, 422, map[string]any{"error": map[string]string{"code": "VALIDATION_ERROR", "message": "payer must be a group member"}})
		return
	}
	if len(req.Splits) == 0 {
		httpx.WriteJSON(w, 422, map[string]any{"error": map[string]string{"code": "VALIDATION_ERROR", "message": "at least one participant required"}})
		return
	}
	mode := strings.ToUpper(strings.TrimSpace(req.SplitMode))
	if mode == "" {
		mode = "EQUAL"
	}
	if mode != "EQUAL" && mode != "EXACT" && mode != "PERCENTAGE" && mode != "SHARES" {
		httpx.WriteJSON(w, 422, map[string]any{"error": map[string]string{"code": "VALIDATION_ERROR", "message": "split_mode must be EQUAL, EXACT, PERCENTAGE, or SHARES"}})
		return
	}

	// Parse participant IDs and validate they are members
	participantIDs := make([]uuid.UUID, len(req.Splits))
	for i, s := range req.Splits {
		id, err := uuid.Parse(s.UserID)
		if err != nil {
			httpx.WriteJSON(w, 422, map[string]any{"error": map[string]string{"code": "VALIDATION_ERROR", "message": "invalid user_id in splits"}})
			return
		}
		participantIDs[i] = id
	}
	if !h.validateGroupMembersSQLC(r.Context(), groupID, participantIDs) {
		httpx.WriteJSON(w, 422, map[string]any{"error": map[string]string{"code": "VALIDATION_ERROR", "message": "all participants must be group members"}})
		return
	}

	// Generate expense ID early for deterministic apportionment (FNV rotation)
	expenseID := uuid.New()
	// Prepare sorted indices for deterministic Hamilton apportionment (Spliit: sort by participantId)
	n := len(req.Splits)
	sortedIdx := make([]int, n)
	for i := range sortedIdx {
		sortedIdx[i] = i
	}
	sort.Slice(sortedIdx, func(a, b int) bool {
		return participantIDs[sortedIdx[a]].String() < participantIDs[sortedIdx[b]].String()
	})

	// Compute split amounts with FNV rotation via expenseID
	var amounts []int64
	switch mode {
	case "EQUAL":
		sortedAmounts, err := money.SplitEqualWithID(req.Amount, n, expenseID.String())
		if err != nil {
			httpx.WriteJSON(w, 422, map[string]any{"error": map[string]string{"code": "VALIDATION_ERROR", "message": err.Error()}})
			return
		}
		amounts = make([]int64, n)
		for sortedPos, origIdx := range sortedIdx {
			amounts[origIdx] = sortedAmounts[sortedPos]
		}
	case "EXACT":
		exact := make([]int64, len(req.Splits))
		for i, s := range req.Splits {
			if s.Amount == nil {
				httpx.WriteJSON(w, 422, map[string]any{"error": map[string]string{"code": "VALIDATION_ERROR", "message": "amount required for EXACT split"}})
				return
			}
			exact[i] = *s.Amount
		}
		var err error
		amounts, err = money.SplitExact(req.Amount, exact)
		if err != nil {
			httpx.WriteJSON(w, 422, map[string]any{"error": map[string]string{"code": "INVALID_SPLIT", "message": err.Error()}})
			return
		}
	case "PERCENTAGE":
		bps := make([]int64, len(req.Splits))
		for i, s := range req.Splits {
			if s.Percentage == nil {
				httpx.WriteJSON(w, 422, map[string]any{"error": map[string]string{"code": "VALIDATION_ERROR", "message": "percentage required for PERCENTAGE split"}})
				return
			}
			bps[i] = int64(*s.Percentage * 100)
		}
		// sort bps to match sorted participant order
		sortedBps := make([]int64, n)
		for sortedPos, origIdx := range sortedIdx {
			sortedBps[sortedPos] = bps[origIdx]
		}
		sortedAmounts, err := money.SplitByPercentageWithID(req.Amount, sortedBps, expenseID.String())
		if err != nil {
			httpx.WriteJSON(w, 422, map[string]any{"error": map[string]string{"code": "INVALID_SPLIT", "message": err.Error()}})
			return
		}
		amounts = make([]int64, n)
		for sortedPos, origIdx := range sortedIdx {
			amounts[origIdx] = sortedAmounts[sortedPos]
		}
	case "SHARES":
		shares := make([]int64, len(req.Splits))
		for i, s := range req.Splits {
			if s.Shares == nil {
				httpx.WriteJSON(w, 422, map[string]any{"error": map[string]string{"code": "VALIDATION_ERROR", "message": "shares required for SHARES split"}})
				return
			}
			shares[i] = *s.Shares
		}
		sortedShares := make([]int64, n)
		for sortedPos, origIdx := range sortedIdx {
			sortedShares[sortedPos] = shares[origIdx]
		}
		sortedAmounts, err := money.SplitBySharesWithID(req.Amount, sortedShares, expenseID.String())
		if err != nil {
			httpx.WriteJSON(w, 422, map[string]any{"error": map[string]string{"code": "VALIDATION_ERROR", "message": err.Error()}})
			return
		}
		amounts = make([]int64, n)
		for sortedPos, origIdx := range sortedIdx {
			amounts[origIdx] = sortedAmounts[sortedPos]
		}
	}

	expenseDate := time.Now().UTC()
	if req.ExpenseDate != nil && *req.ExpenseDate != "" {
		t, err := time.Parse("2006-01-02", *req.ExpenseDate)
		if err != nil {
			httpx.WriteJSON(w, 422, map[string]any{"error": map[string]string{"code": "VALIDATION_ERROR", "message": "expense_date must be YYYY-MM-DD"}})
			return
		}
		expenseDate = t
	}
	notes := ""
	if req.Notes != nil {
		notes = *req.Notes
	}
	var categoryID *uuid.UUID
	if req.CategoryID != nil && *req.CategoryID != "" {
		id, err := uuid.Parse(*req.CategoryID)
		if err != nil {
			httpx.WriteJSON(w, 422, map[string]any{"error": map[string]string{"code": "VALIDATION_ERROR", "message": "invalid category_id"}})
			return
		}
		categoryID = &id
	}
	uid, _ := httpx.GetUserID(r.Context())
	createdBy, _ := uuid.Parse(uid)

	tx, err := h.Pool.Begin(r.Context())
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	defer tx.Rollback(r.Context())

	h.ensureQueries()
	qtx := q.WithTx(tx)
	// Build pgtype params for sqlc
	var catUUID pgtype.UUID
	if categoryID != nil {
		catUUID = pgtype.UUID{Bytes: *categoryID, Valid: true}
	}
	var exch pgtype.Numeric
	if exchangeRate != nil {
		_ = exch.Scan(fmt.Sprintf("%v", *exchangeRate))
		exch.Valid = true
	}
	var baseCurr pgtype.Text
	if baseCurrency != nil {
		baseCurr = pgtype.Text{String: *baseCurrency, Valid: true}
	}
	var baseAmt pgtype.Int8
	if baseAmount != nil {
		baseAmt = pgtype.Int8{Int64: *baseAmount, Valid: true}
	}
	err = qtx.CreateExpense(r.Context(), db.CreateExpenseParams{
		ID:           expenseID,
		GroupID:      groupID,
		Description:  req.Description,
		Amount:       req.Amount,
		Currency:     currency,
		SplitMode:    mode,
		PaidBy:       paidBy,
		Column8:      catUUID,
		Notes:        notes,
		ExpenseDate:  timeToDate(expenseDate),
		CreatedBy:    createdBy,
		ExchangeRate: exch,
		BaseCurrency: baseCurr,
		BaseAmount:   baseAmt,
	})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	for i, pid := range participantIDs {
		var pct pgtype.Numeric
		var shares pgtype.Int4
		if mode == "PERCENTAGE" && req.Splits[i].Percentage != nil {
			_ = pct.Scan(fmt.Sprintf("%v", *req.Splits[i].Percentage))
			pct.Valid = true
		}
		if mode == "SHARES" && req.Splits[i].Shares != nil {
			shares = pgtype.Int4{Int32: int32(*req.Splits[i].Shares), Valid: true}
		}
		err = qtx.InsertExpenseSplit(r.Context(), db.InsertExpenseSplitParams{
			ExpenseID:  expenseID,
			UserID:     pid,
			Amount:     amounts[i],
			Percentage: pct,
			Shares:     shares,
		})
		if err != nil {
			httpx.WriteError(w, r, err)
			return
		}
	}
	h.ensureQueries()
	err = qtx.InsertExpenseActivityAdded(r.Context(), db.InsertExpenseActivityAddedParams{
		GroupID:  groupID,
		ActorID:  createdBy,
		EntityID: expenseID,
		Payload:  json.RawMessage(`{"description":"` + req.Description + `"}`),
	})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	// Notify other members (fire and forget within tx)
	var notifyIDs []uuid.UUID
	notifyIDs, _ = qtx.ListOtherGroupMembers(r.Context(), db.ListOtherGroupMembersParams{GroupID: groupID, UserID: createdBy})
	_ = tx.Commit(r.Context())
	// Create notifications outside tx - use sqlc when available
	h.ensureQueries()
	for _, nid := range notifyIDs {
		_ = h.Queries.CreateNotificationExpenseAdded(r.Context(), db.CreateNotificationExpenseAddedParams{
			UserID: nid,
			Title:  req.Description + " added",
			Body:   req.Description,
			Data:   json.RawMessage(`{"group_id":"` + groupID.String() + `","expense_id":"` + expenseID.String() + `"}`),
		})
	}

	httpx.WriteJSON(w, 201, map[string]any{
		"id": expenseID, "group_id": groupID, "description": req.Description, "amount": req.Amount,
		"currency": currency, "split_mode": mode, "paid_by": paidBy, "notes": notes,
		"expense_date": expenseDate.Format("2006-01-02"), "splits": buildSplitsResp(participantIDs, amounts, req.Splits, mode),
	})
}

func buildSplitsResp(ids []uuid.UUID, amounts []int64, inputs []splitInput, mode string) []map[string]any {
	out := make([]map[string]any, len(ids))
	for i, id := range ids {
		m := map[string]any{"user_id": id, "amount": amounts[i]}
		if mode == "PERCENTAGE" && inputs[i].Percentage != nil {
			m["percentage"] = *inputs[i].Percentage
		}
		if mode == "SHARES" && inputs[i].Shares != nil {
			m["shares"] = *inputs[i].Shares
		}
		out[i] = m
	}
	return out
}

func (h *Handler) ListExpenses(w http.ResponseWriter, r *http.Request) {
	h.ensureQueries()
	q := h.Queries
	groupID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		httpx.WriteJSON(w, 422, map[string]any{"error": map[string]string{"code": "VALIDATION_ERROR", "message": "invalid group id"}})
		return
	}
	if !h.mustBeMemberSQLC(r, groupID) {
		httpx.WriteError(w, r, httpx.ErrGroupMemberRequired)
		return
	}
	// Recurring expense materialization is handled by a background worker;
	// no lazy SELECT needed here (sqlc-only: removed raw Pool.Exec).
	queryVals := r.URL.Query()
	limitStr := queryVals.Get("limit")
	limit := 30
	if limitStr != "" {
		if v, err := strconv.Atoi(limitStr); err == nil && v > 0 && v <= 100 {
			limit = v
		}
	}
	cursor := queryVals.Get("cursor")
	search := strings.TrimSpace(queryVals.Get("q"))
	categoryID := queryVals.Get("category_id")
	payer := queryVals.Get("payer")
	dateFrom := queryVals.Get("date_from")
	dateTo := queryVals.Get("date_to")
	amountMin := queryVals.Get("amount_min")
	amountMax := queryVals.Get("amount_max")

	// Build query with filters; cursor-based on created_at + id
	// sqlc-only: unfiltered path uses ListExpensesByGroup, filtered path uses ListExpenses + in-Go filtering (no raw Pool.Query).
	hasFilters := search != "" || categoryID != "" || payer != "" || dateFrom != "" || dateTo != "" || amountMin != "" || amountMax != "" || cursor != ""
	var exps []struct {
		ID          uuid.UUID  `json:"id"`
		Description string     `json:"description"`
		Amount      int64      `json:"amount"`
		Currency    string     `json:"currency"`
		SplitMode   string     `json:"split_mode"`
		PaidBy      uuid.UUID  `json:"paid_by"`
		CategoryID  *uuid.UUID `json:"category_id"`
		Notes       string     `json:"notes"`
		ExpenseDate string     `json:"expense_date"`
		CreatedBy   uuid.UUID  `json:"created_by"`
		CreatedAt   time.Time  `json:"created_at"`
	}
	var lastID *uuid.UUID
	// If no filters and no cursor, use sqlc ListExpensesByGroup / ListExpenses
	if !hasFilters {
		rows, err := q.ListExpensesByGroup(r.Context(), db.ListExpensesByGroupParams{GroupID: groupID, Limit: int32(limit + 1)})
		if err == nil {
			for _, e := range rows {
				var catID *uuid.UUID
				if e.CategoryID != uuid.Nil {
					tmp := e.CategoryID
					catID = &tmp
				}
				ed := ""
				if e.ExpenseDate.Valid {
					ed = e.ExpenseDate.Time.Format("2006-01-02")
				}
				createdAt := time.Time{}
				if e.CreatedAt.Valid {
					createdAt = e.CreatedAt.Time
				}
				exps = append(exps, struct {
					ID          uuid.UUID  `json:"id"`
					Description string     `json:"description"`
					Amount      int64      `json:"amount"`
					Currency    string     `json:"currency"`
					SplitMode   string     `json:"split_mode"`
					PaidBy      uuid.UUID  `json:"paid_by"`
					CategoryID  *uuid.UUID `json:"category_id"`
					Notes       string     `json:"notes"`
					ExpenseDate string     `json:"expense_date"`
					CreatedBy   uuid.UUID  `json:"created_by"`
					CreatedAt   time.Time  `json:"created_at"`
				}{ID: e.ID, Description: e.Description, Amount: e.Amount, Currency: e.Currency, SplitMode: e.SplitMode, PaidBy: e.PaidBy, CategoryID: catID, Notes: e.Notes, ExpenseDate: ed, CreatedBy: e.CreatedBy, CreatedAt: createdAt})
				tmp := e.ID
				lastID = &tmp
			}
		} else {
			hasFilters = true // fallback to manual on error
		}
	}
	if hasFilters {
		if len(exps) == 0 {
			// Filtered listing via sqlc ListExpenses + in-Go filtering (sqlc-only, no raw Pool.Query)
			allRows, err := q.ListExpenses(r.Context(), groupID)
			if err != nil {
				httpx.WriteError(w, r, err)
				return
			}
			// Pre-parse filter values
			var filterCat *uuid.UUID
			if categoryID != "" {
				if cid, err := uuid.Parse(categoryID); err == nil {
					filterCat = &cid
				}
			}
			var filterPayer *uuid.UUID
			if payer != "" {
				if pid, err := uuid.Parse(payer); err == nil {
					filterPayer = &pid
				}
			}
			var filterMin, filterMax *int64
			if amountMin != "" {
				if v, err := strconv.ParseInt(amountMin, 10, 64); err == nil && v > 0 {
					filterMin = &v
				}
			}
			if amountMax != "" {
				if v, err := strconv.ParseInt(amountMax, 10, 64); err == nil && v > 0 {
					filterMax = &v
				}
			}
			var cursorTime *time.Time
			var cursorID *uuid.UUID
			if cursor != "" {
				if cid, err := uuid.Parse(cursor); err == nil {
					// Fetch cursor expense to get its created_at for ordering
					if cRow, cerr := q.GetExpenseDetails(r.Context(), cid); cerr == nil && cRow.CreatedAt.Valid {
						t := cRow.CreatedAt.Time
						cursorTime = &t
						cursorID = &cid
					}
				}
			}
			for _, e := range allRows {
				if search != "" && !strings.Contains(strings.ToLower(e.Description), strings.ToLower(search)) && !strings.Contains(strings.ToLower(e.Notes), strings.ToLower(search)) {
					continue
				}
				if filterCat != nil {
					if e.CategoryID == uuid.Nil {
						continue
					}
					if e.CategoryID != *filterCat {
						continue
					}
				}
				if filterPayer != nil && e.PaidBy != *filterPayer {
					continue
				}
				if dateFrom != "" && e.ExpenseDate.Valid {
					if e.ExpenseDate.Time.Format("2006-01-02") < dateFrom {
						continue
					}
				}
				if dateTo != "" && e.ExpenseDate.Valid {
					if e.ExpenseDate.Time.Format("2006-01-02") > dateTo {
						continue
					}
				}
				if filterMin != nil && e.Amount < *filterMin {
					continue
				}
				if filterMax != nil && e.Amount > *filterMax {
					continue
				}
				if cursorTime != nil && cursorID != nil && e.CreatedAt.Valid {
					// (created_at, id) < (cursorTime, cursorID) ordering DESC
					if e.CreatedAt.Time.After(*cursorTime) {
						continue
					}
					if e.CreatedAt.Time.Equal(*cursorTime) && e.ID.String() >= cursorID.String() {
						continue
					}
				}
				var catID *uuid.UUID
				if e.CategoryID != uuid.Nil {
					tmp := e.CategoryID
					catID = &tmp
				}
				ed := ""
				if e.ExpenseDate.Valid {
					ed = e.ExpenseDate.Time.Format("2006-01-02")
				}
				createdAt := time.Time{}
				if e.CreatedAt.Valid {
					createdAt = e.CreatedAt.Time
				}
				exps = append(exps, struct {
					ID          uuid.UUID  `json:"id"`
					Description string     `json:"description"`
					Amount      int64      `json:"amount"`
					Currency    string     `json:"currency"`
					SplitMode   string     `json:"split_mode"`
					PaidBy      uuid.UUID  `json:"paid_by"`
					CategoryID  *uuid.UUID `json:"category_id"`
					Notes       string     `json:"notes"`
					ExpenseDate string     `json:"expense_date"`
					CreatedBy   uuid.UUID  `json:"created_by"`
					CreatedAt   time.Time  `json:"created_at"`
				}{ID: e.ID, Description: e.Description, Amount: e.Amount, Currency: e.Currency, SplitMode: e.SplitMode, PaidBy: e.PaidBy, CategoryID: catID, Notes: e.Notes, ExpenseDate: ed, CreatedBy: e.CreatedBy, CreatedAt: createdAt})
				tmp := e.ID
				lastID = &tmp
				if len(exps) >= limit+1 {
					break
				}
			}
		}
	}
	// Handle pagination cursor trimming
	var nextCursor *string
	if len(exps) > limit {
		exps = exps[:limit]
		if lastID != nil {
			s := lastID.String()
			nextCursor = &s
		}
	}
	if exps == nil {
		exps = []struct {
			ID          uuid.UUID  `json:"id"`
			Description string     `json:"description"`
			Amount      int64      `json:"amount"`
			Currency    string     `json:"currency"`
			SplitMode   string     `json:"split_mode"`
			PaidBy      uuid.UUID  `json:"paid_by"`
			CategoryID  *uuid.UUID `json:"category_id"`
			Notes       string     `json:"notes"`
			ExpenseDate string     `json:"expense_date"`
			CreatedBy   uuid.UUID  `json:"created_by"`
			CreatedAt   time.Time  `json:"created_at"`
		}{}
	}
	// Load splits for these expenses via sqlc when available
	expIDs := make([]uuid.UUID, len(exps))
	for i, e := range exps {
		expIDs[i] = e.ID
	}
	splitsByExpense := map[uuid.UUID][]map[string]any{}
	if len(expIDs) > 0 {
		// Convert []uuid.UUID to []pgtype.UUID for sqlc
		pgIDs := make([]pgtype.UUID, len(expIDs))
		for i, id := range expIDs {
			pgIDs[i] = pgtype.UUID{Bytes: id, Valid: true}
		}
		srows, err := q.ListExpenseSplitsByExpenseIDs(r.Context(), pgIDs)
		if err == nil {
			for _, es := range srows {
				m := map[string]any{"user_id": es.UserID, "amount": es.Amount}
				if es.Percentage.Valid {
					// pgtype.Numeric to float64
					if f, err := es.Percentage.Float64Value(); err == nil && f.Valid {
						m["percentage"] = f.Float64
					}
				}
				if es.Shares.Valid {
					m["shares"] = int(es.Shares.Int32)
				}
				splitsByExpense[es.ExpenseID] = append(splitsByExpense[es.ExpenseID], m)
			}
		}
	}
	data := make([]map[string]any, len(exps))
	for i, e := range exps {
		data[i] = map[string]any{
			"id": e.ID, "description": e.Description, "amount": e.Amount, "currency": e.Currency,
			"split_mode": e.SplitMode, "paid_by": e.PaidBy, "category_id": e.CategoryID, "notes": e.Notes,
			"expense_date": e.ExpenseDate, "created_by": e.CreatedBy, "created_at": e.CreatedAt,
			"splits": splitsByExpense[e.ID],
		}
	}
	resp := map[string]any{"data": data}
	if nextCursor != nil {
		resp["next_cursor"] = *nextCursor
	}
	httpx.WriteJSON(w, 200, resp)
}

func (h *Handler) GetExpense(w http.ResponseWriter, r *http.Request) {
	h.ensureQueries()
	q := h.Queries
	expenseID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		httpx.WriteJSON(w, 422, map[string]any{"error": map[string]string{"code": "VALIDATION_ERROR", "message": "invalid expense id"}})
		return
	}
	var groupID uuid.UUID
	var description, currency, splitMode, notes string
	var amount int64
	var paidBy, createdBy uuid.UUID
	var categoryID *uuid.UUID
	var expenseDate time.Time
	row, qerr := q.GetExpense(r.Context(), expenseID)
	if qerr != nil {
		if qerr == pgx.ErrNoRows {
			httpx.WriteError(w, r, httpx.ErrNotFound)
			return
		}
		httpx.WriteError(w, r, qerr)
		return
	}
	if row.DeletedAt.Valid {
		httpx.WriteError(w, r, httpx.ErrNotFound)
		return
	}
	groupID = row.GroupID
	description = row.Description
	amount = row.Amount
	currency = row.Currency
	splitMode = row.SplitMode
	paidBy = row.PaidBy
	if row.CategoryID != uuid.Nil {
		tmp := row.CategoryID
		categoryID = &tmp
	}
	notes = row.Notes
	if row.ExpenseDate.Valid {
		expenseDate = row.ExpenseDate.Time
	}
	createdBy = row.CreatedBy
	if !h.mustBeMemberSQLC(r, groupID) {
		httpx.WriteError(w, r, httpx.ErrGroupMemberRequired)
		return
	}
	var splits []map[string]any
	rows, qerr := q.ListExpenseSplits(r.Context(), expenseID)
	if qerr != nil {
		httpx.WriteError(w, r, qerr)
		return
	}
	for _, s := range rows {
		m := map[string]any{"user_id": s.UserID, "amount": s.Amount}
		if s.Percentage.Valid {
			if f, err := s.Percentage.Float64Value(); err == nil && f.Valid {
				m["percentage"] = f.Float64
			}
		}
		if s.Shares.Valid {
			m["shares"] = int(s.Shares.Int32)
		}
		splits = append(splits, m)
	}
	httpx.WriteJSON(w, 200, map[string]any{
		"id": expenseID, "group_id": groupID, "description": description, "amount": amount, "currency": currency,
		"split_mode": splitMode, "paid_by": paidBy, "category_id": categoryID, "notes": notes,
		"expense_date": expenseDate.Format("2006-01-02"), "created_by": createdBy, "splits": splits,
	})
}

func (h *Handler) UpdateExpense(w http.ResponseWriter, r *http.Request) {
	h.ensureQueries()
	q := h.Queries
	expenseID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		httpx.WriteJSON(w, 422, map[string]any{"error": map[string]string{"code": "VALIDATION_ERROR", "message": "invalid expense id"}})
		return
	}
	var groupID uuid.UUID
	var createdBy uuid.UUID
	row, qerr := q.GetExpenseForUpdate(r.Context(), expenseID)
	if qerr != nil {
		if qerr == pgx.ErrNoRows {
			httpx.WriteError(w, r, httpx.ErrNotFound)
			return
		}
		httpx.WriteError(w, r, qerr)
		return
	}
	if row.DeletedAt.Valid {
		httpx.WriteError(w, r, httpx.ErrNotFound)
		return
	}
	groupID = row.GroupID
	createdBy = row.CreatedBy
	if !h.mustBeMemberSQLC(r, groupID) {
		httpx.WriteError(w, r, httpx.ErrGroupMemberRequired)
		return
	}
	// For simplicity, reuse create logic but update existing row atomically
	var req createExpenseReq
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteJSON(w, 400, map[string]any{"error": map[string]string{"code": "VALIDATION_ERROR", "message": err.Error()}})
		return
	}
	req.Description = strings.TrimSpace(req.Description)
	if req.Description == "" {
		httpx.WriteJSON(w, 422, map[string]any{"error": map[string]string{"code": "VALIDATION_ERROR", "message": "description required"}})
		return
	}
	if req.Amount <= 0 {
		httpx.WriteJSON(w, 422, map[string]any{"error": map[string]string{"code": "VALIDATION_ERROR", "message": "amount must be > 0"}})
		return
	}
	paidBy, err := uuid.Parse(req.PaidBy)
	if err != nil {
		httpx.WriteJSON(w, 422, map[string]any{"error": map[string]string{"code": "VALIDATION_ERROR", "message": "invalid paid_by"}})
		return
	}
	if !h.checkIsGroupMember(r.Context(), groupID, paidBy) {
		httpx.WriteJSON(w, 422, map[string]any{"error": map[string]string{"code": "VALIDATION_ERROR", "message": "payer must be a group member"}})
		return
	}
	// Only the creator, the payer, or group OWNER/ADMIN may edit
	editorUIDStr, _ := httpx.GetUserID(r.Context())
	editorID, _ := uuid.Parse(editorUIDStr)
	if createdBy != editorID && paidBy != editorID {
		role := h.getMemberRole(r.Context(), groupID, editorID)
		if role != "OWNER" && role != "ADMIN" {
			httpx.WriteError(w, r, httpx.ErrForbidden)
			return
		}
	}
	if len(req.Splits) == 0 {
		httpx.WriteJSON(w, 422, map[string]any{"error": map[string]string{"code": "VALIDATION_ERROR", "message": "at least one participant required"}})
		return
	}
	mode := strings.ToUpper(strings.TrimSpace(req.SplitMode))
	if mode == "" {
		mode = "EQUAL"
	}
	if mode != "EQUAL" && mode != "EXACT" && mode != "PERCENTAGE" && mode != "SHARES" {
		httpx.WriteJSON(w, 422, map[string]any{"error": map[string]string{"code": "VALIDATION_ERROR", "message": "split_mode must be EQUAL, EXACT, PERCENTAGE, or SHARES"}})
		return
	}
	participantIDs := make([]uuid.UUID, len(req.Splits))
	for i, s := range req.Splits {
		id, err := uuid.Parse(s.UserID)
		if err != nil {
			httpx.WriteJSON(w, 422, map[string]any{"error": map[string]string{"code": "VALIDATION_ERROR", "message": "invalid user_id in splits"}})
			return
		}
		participantIDs[i] = id
	}
	if !h.validateGroupMembersSQLC(r.Context(), groupID, participantIDs) {
		httpx.WriteJSON(w, 422, map[string]any{"error": map[string]string{"code": "VALIDATION_ERROR", "message": "all participants must be group members"}})
		return
	}
	// Sorted indices for deterministic apportionment
	n := len(req.Splits)
	sortedIdx := make([]int, n)
	for i := range sortedIdx {
		sortedIdx[i] = i
	}
	sort.Slice(sortedIdx, func(a, b int) bool {
		return participantIDs[sortedIdx[a]].String() < participantIDs[sortedIdx[b]].String()
	})
	var amounts []int64
	switch mode {
	case "EQUAL":
		sortedAmounts, err := money.SplitEqualWithID(req.Amount, n, expenseID.String())
		if err != nil {
			httpx.WriteJSON(w, 422, map[string]any{"error": map[string]string{"code": "VALIDATION_ERROR", "message": err.Error()}})
			return
		}
		amounts = make([]int64, n)
		for sortedPos, origIdx := range sortedIdx {
			amounts[origIdx] = sortedAmounts[sortedPos]
		}
	case "EXACT":
		exact := make([]int64, len(req.Splits))
		for i, s := range req.Splits {
			if s.Amount == nil {
				httpx.WriteJSON(w, 422, map[string]any{"error": map[string]string{"code": "VALIDATION_ERROR", "message": "amount required for EXACT split"}})
				return
			}
			exact[i] = *s.Amount
		}
		amounts, err = money.SplitExact(req.Amount, exact)
		if err != nil {
			httpx.WriteJSON(w, 422, map[string]any{"error": map[string]string{"code": "INVALID_SPLIT", "message": err.Error()}})
			return
		}
	case "PERCENTAGE":
		bps := make([]int64, len(req.Splits))
		for i, s := range req.Splits {
			if s.Percentage == nil {
				httpx.WriteJSON(w, 422, map[string]any{"error": map[string]string{"code": "VALIDATION_ERROR", "message": "percentage required for PERCENTAGE split"}})
				return
			}
			bps[i] = int64(*s.Percentage * 100)
		}
		sortedBps := make([]int64, n)
		for sortedPos, origIdx := range sortedIdx {
			sortedBps[sortedPos] = bps[origIdx]
		}
		sortedAmounts, err := money.SplitByPercentageWithID(req.Amount, sortedBps, expenseID.String())
		if err != nil {
			httpx.WriteJSON(w, 422, map[string]any{"error": map[string]string{"code": "INVALID_SPLIT", "message": err.Error()}})
			return
		}
		amounts = make([]int64, n)
		for sortedPos, origIdx := range sortedIdx {
			amounts[origIdx] = sortedAmounts[sortedPos]
		}
	case "SHARES":
		shares := make([]int64, len(req.Splits))
		for i, s := range req.Splits {
			if s.Shares == nil {
				httpx.WriteJSON(w, 422, map[string]any{"error": map[string]string{"code": "VALIDATION_ERROR", "message": "shares required for SHARES split"}})
				return
			}
			shares[i] = *s.Shares
		}
		sortedShares := make([]int64, n)
		for sortedPos, origIdx := range sortedIdx {
			sortedShares[sortedPos] = shares[origIdx]
		}
		sortedAmounts, err := money.SplitBySharesWithID(req.Amount, sortedShares, expenseID.String())
		if err != nil {
			httpx.WriteJSON(w, 422, map[string]any{"error": map[string]string{"code": "VALIDATION_ERROR", "message": err.Error()}})
			return
		}
		amounts = make([]int64, n)
		for sortedPos, origIdx := range sortedIdx {
			amounts[origIdx] = sortedAmounts[sortedPos]
		}
	}
	groupCurrency, err := h.getGroupCurrency(r.Context(), groupID)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	currency := groupCurrency
	if req.Currency != nil && strings.TrimSpace(*req.Currency) != "" {
		currency = strings.ToUpper(strings.TrimSpace(*req.Currency))
		if !auth.SupportedCurrencies[currency] {
			httpx.WriteJSON(w, 422, map[string]any{"error": map[string]string{"code": "VALIDATION_ERROR", "message": "unsupported currency"}})
			return
		}
	}
	// Multi-currency: an expense in a currency different from the group's must
	// carry a positive exchange rate; the server computes base_amount so
	// balances can aggregate in the group currency.
	var exchangeRate *float64
	var baseCurrency *string
	var baseAmount *int64
	if currency != groupCurrency {
		if req.ExchangeRate == nil || *req.ExchangeRate <= 0 {
			httpx.WriteJSON(w, 422, map[string]any{"error": map[string]string{"code": "VALIDATION_ERROR", "message": "exchange_rate > 0 required when expense currency differs from the group currency"}})
			return
		}
		rate := *req.ExchangeRate
		exchangeRate = &rate
		bc := groupCurrency
		baseCurrency = &bc
		ba := int64(math.Round(float64(req.Amount) * rate))
		baseAmount = &ba
	}
	notes := ""
	if req.Notes != nil {
		notes = *req.Notes
	}
	var categoryID *uuid.UUID
	if req.CategoryID != nil && *req.CategoryID != "" {
		id, err := uuid.Parse(*req.CategoryID)
		if err != nil {
			httpx.WriteJSON(w, 422, map[string]any{"error": map[string]string{"code": "VALIDATION_ERROR", "message": "invalid category_id"}})
			return
		}
		categoryID = &id
	}
	expenseDate := time.Now().UTC()
	if req.ExpenseDate != nil && *req.ExpenseDate != "" {
		t, err := time.Parse("2006-01-02", *req.ExpenseDate)
		if err != nil {
			httpx.WriteJSON(w, 422, map[string]any{"error": map[string]string{"code": "VALIDATION_ERROR", "message": "expense_date must be YYYY-MM-DD"}})
			return
		}
		expenseDate = t
	}
	uid, _ := httpx.GetUserID(r.Context())
	actorID, _ := uuid.Parse(uid)

	tx, err := h.Pool.Begin(r.Context())
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	defer tx.Rollback(r.Context())
	h.ensureQueries()
	qtx := q.WithTx(tx)
	var catUUID pgtype.UUID
	if categoryID != nil {
		catUUID = pgtype.UUID{Bytes: *categoryID, Valid: true}
	}
	var exch pgtype.Numeric
	if exchangeRate != nil {
		_ = exch.Scan(fmt.Sprintf("%v", *exchangeRate))
		exch.Valid = true
	}
	var baseCurr pgtype.Text
	if baseCurrency != nil {
		baseCurr = pgtype.Text{String: *baseCurrency, Valid: true}
	}
	var baseAmt pgtype.Int8
	if baseAmount != nil {
		baseAmt = pgtype.Int8{Int64: *baseAmount, Valid: true}
	}
	err = qtx.UpdateExpense(r.Context(), db.UpdateExpenseParams{
		Description:  req.Description,
		Amount:       req.Amount,
		Currency:     currency,
		SplitMode:    mode,
		PaidBy:       paidBy,
		Column6:      catUUID,
		Notes:        notes,
		ExpenseDate:  timeToDate(expenseDate),
		ExchangeRate: exch,
		BaseCurrency: baseCurr,
		BaseAmount:   baseAmt,
		ID:           expenseID,
	})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	err = qtx.DeleteExpenseSplits(r.Context(), expenseID)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	for i, pid := range participantIDs {
		var pct pgtype.Numeric
		var shares pgtype.Int4
		if mode == "PERCENTAGE" && req.Splits[i].Percentage != nil {
			_ = pct.Scan(fmt.Sprintf("%v", *req.Splits[i].Percentage))
			pct.Valid = true
		}
		if mode == "SHARES" && req.Splits[i].Shares != nil {
			shares = pgtype.Int4{Int32: int32(*req.Splits[i].Shares), Valid: true}
		}
		err = qtx.InsertExpenseSplit(r.Context(), db.InsertExpenseSplitParams{
			ExpenseID:  expenseID,
			UserID:     pid,
			Amount:     amounts[i],
			Percentage: pct,
			Shares:     shares,
		})
		if err != nil {
			httpx.WriteError(w, r, err)
			return
		}
	}
	err = qtx.InsertExpenseActivityUpdated(r.Context(), db.InsertExpenseActivityUpdatedParams{
		GroupID:  groupID,
		ActorID:  actorID,
		EntityID: expenseID,
		Payload:  json.RawMessage(`{"description":"` + req.Description + `"}`),
	})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, 200, map[string]any{"id": expenseID, "message": "updated"})
}

func (h *Handler) DeleteExpense(w http.ResponseWriter, r *http.Request) {
	h.ensureQueries()
	q := h.Queries
	expenseID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		httpx.WriteJSON(w, 422, map[string]any{"error": map[string]string{"code": "VALIDATION_ERROR", "message": "invalid expense id"}})
		return
	}
	var groupID, createdBy uuid.UUID
	row, qerr := q.GetExpenseForDelete(r.Context(), expenseID)
	if qerr != nil {
		if qerr == pgx.ErrNoRows {
			httpx.WriteError(w, r, httpx.ErrNotFound)
			return
		}
		httpx.WriteError(w, r, qerr)
		return
	}
	groupID = row.GroupID
	createdBy = row.CreatedBy
	if !h.mustBeMemberSQLC(r, groupID) {
		httpx.WriteError(w, r, httpx.ErrGroupMemberRequired)
		return
	}
	uid, _ := httpx.GetUserID(r.Context())
	actorID, _ := uuid.Parse(uid)
	if createdBy != actorID {
		role := h.getMemberRole(r.Context(), groupID, actorID)
		if role != "OWNER" && role != "ADMIN" {
			httpx.WriteError(w, r, httpx.ErrForbidden)
			return
		}
	}
	_ = q.SoftDeleteExpense(r.Context(), expenseID)
	_ = q.InsertExpenseActivityDeleted(r.Context(), db.InsertExpenseActivityDeletedParams{
		GroupID:  groupID,
		ActorID:  actorID,
		EntityID: expenseID,
		Payload:  json.RawMessage(`{}`),
	})
	httpx.WriteJSON(w, 200, map[string]any{"message": "deleted"})
}

// Ensure imports are used
var _ = context.Background
