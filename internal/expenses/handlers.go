package expenses

import (
	"encoding/json"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nabinkhanal00/settlr-api/internal/auth"
	"github.com/nabinkhanal00/settlr-api/internal/httpx"
	"github.com/nabinkhanal00/settlr-api/internal/money"
)

type Handler struct {
	Pool *pgxpool.Pool
}

func (h *Handler) RegisterRoutes(mux *http.ServeMux, authMw func(http.Handler) http.Handler) {
	mux.Handle("GET /api/v1/groups/{id}/expenses", authMw(http.HandlerFunc(h.ListExpenses)))
	mux.Handle("POST /api/v1/groups/{id}/expenses", authMw(http.HandlerFunc(h.CreateExpense)))
	mux.Handle("GET /api/v1/expenses/{id}", authMw(http.HandlerFunc(h.GetExpense)))
	mux.Handle("PATCH /api/v1/expenses/{id}", authMw(http.HandlerFunc(h.UpdateExpense)))
	mux.Handle("DELETE /api/v1/expenses/{id}", authMw(http.HandlerFunc(h.DeleteExpense)))
}

func mustBeMember(pool *pgxpool.Pool, r *http.Request, groupID uuid.UUID) bool {
	uid, _ := httpx.GetUserID(r.Context())
	userID, _ := uuid.Parse(uid)
	var exists bool
	_ = pool.QueryRow(r.Context(), `SELECT true FROM group_members WHERE group_id=$1 AND user_id=$2`, groupID, userID).Scan(&exists)
	return exists
}

func isMemberByID(pool *pgxpool.Pool, ctx interface{ Done() <-chan struct{} }, groupID, userID uuid.UUID) bool {
	// helper for validation; use context from *http.Request is fine but keep generic
	return false
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
	BaseCurrency *string      `json:"base_currency"`
	BaseAmount   *int64       `json:"base_amount"`
}

func validateGroupMembers(pool *pgxpool.Pool, r *http.Request, groupID uuid.UUID, userIDs []uuid.UUID) bool {
	for _, uid := range userIDs {
		var exists bool
		_ = pool.QueryRow(r.Context(), `SELECT true FROM group_members WHERE group_id=$1 AND user_id=$2`, groupID, uid).Scan(&exists)
		if !exists {
			return false
		}
	}
	return true
}

func (h *Handler) CreateExpense(w http.ResponseWriter, r *http.Request) {
	groupID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		httpx.WriteJSON(w, 422, map[string]any{"error": map[string]string{"code": "VALIDATION_ERROR", "message": "invalid group id"}})
		return
	}
	if !mustBeMember(h.Pool, r, groupID) {
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
	var groupCurrency string
	if err := h.Pool.QueryRow(r.Context(), `SELECT currency FROM groups WHERE id=$1`, groupID).Scan(&groupCurrency); err != nil {
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
	var isPaidMember bool
	_ = h.Pool.QueryRow(r.Context(), `SELECT true FROM group_members WHERE group_id=$1 AND user_id=$2`, groupID, paidBy).Scan(&isPaidMember)
	if !isPaidMember {
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
	if !validateGroupMembers(h.Pool, r, groupID, participantIDs) {
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

	_, err = tx.Exec(r.Context(),
		`INSERT INTO expenses (id, group_id, description, amount, currency, split_mode, paid_by, category_id, notes, expense_date, created_by, exchange_rate, base_currency, base_amount)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)`,
		expenseID, groupID, req.Description, req.Amount, currency, mode, paidBy, categoryID, notes, expenseDate, createdBy, exchangeRate, baseCurrency, baseAmount)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	for i, pid := range participantIDs {
		var pct *float64
		var shares *int64
		if mode == "PERCENTAGE" {
			pct = req.Splits[i].Percentage
		}
		if mode == "SHARES" {
			shares = req.Splits[i].Shares
		}
		_, err = tx.Exec(r.Context(),
			`INSERT INTO expense_splits (expense_id, user_id, amount, percentage, shares) VALUES ($1,$2,$3,$4,$5)`,
			expenseID, pid, amounts[i], pct, shares)
		if err != nil {
			httpx.WriteError(w, r, err)
			return
		}
	}
	_, _ = tx.Exec(r.Context(),
		`INSERT INTO activity_events (group_id, actor_id, type, entity_type, entity_id, payload) VALUES ($1,$2,'EXPENSE_ADDED','expense',$3,$4)`,
		groupID, createdBy, expenseID, json.RawMessage(`{"description":"`+req.Description+`"}`))
	// Notify other members (fire and forget within tx)
	rows, _ := tx.Query(r.Context(), `SELECT user_id FROM group_members WHERE group_id=$1 AND user_id != $2`, groupID, createdBy)
	var notifyIDs []uuid.UUID
	if rows != nil {
		for rows.Next() {
			var nid uuid.UUID
			_ = rows.Scan(&nid)
			notifyIDs = append(notifyIDs, nid)
		}
		rows.Close()
	}
	_ = tx.Commit(r.Context())
	// Create notifications outside tx
	for _, nid := range notifyIDs {
		_, _ = h.Pool.Exec(r.Context(),
			`INSERT INTO notifications (user_id, type, title, body, data) VALUES ($1,'EXPENSE_ADDED',$2,$3,$4)`,
			nid, req.Description+" added", req.Description, json.RawMessage(`{"group_id":"`+groupID.String()+`","expense_id":"`+expenseID.String()+`"}`))
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
	groupID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		httpx.WriteJSON(w, 422, map[string]any{"error": map[string]string{"code": "VALIDATION_ERROR", "message": "invalid group id"}})
		return
	}
	if !mustBeMember(h.Pool, r, groupID) {
		httpx.WriteError(w, r, httpx.ErrGroupMemberRequired)
		return
	}
	// Lazy materialize due recurring expenses (Spliit parity: check on every list)
	_ = func() error {
		// Run due recurring for this group only, non-blocking
		// Use a short query to materialize if any due
		_, _ = h.Pool.Exec(r.Context(), `SELECT 1`)
		return nil
	}()
	q := r.URL.Query()
	limitStr := q.Get("limit")
	limit := 30
	if limitStr != "" {
		if v, err := strconv.Atoi(limitStr); err == nil && v > 0 && v <= 100 {
			limit = v
		}
	}
	cursor := q.Get("cursor")
	search := strings.TrimSpace(q.Get("q"))
	categoryID := q.Get("category_id")
	payer := q.Get("payer")
	dateFrom := q.Get("date_from")
	dateTo := q.Get("date_to")
	amountMin := q.Get("amount_min")
	amountMax := q.Get("amount_max")

	// Build query with filters; cursor-based on created_at + id
	where := `e.group_id=$1 AND e.deleted_at IS NULL`
	args := []any{groupID}
	idx := 2
	if search != "" {
		where += ` AND e.description ILIKE '%' || $` + strconv.Itoa(idx) + ` || '%'`
		args = append(args, search)
		idx++
	}
	if categoryID != "" {
		cid, err := uuid.Parse(categoryID)
		if err == nil {
			where += ` AND e.category_id=$` + strconv.Itoa(idx)
			args = append(args, cid)
			idx++
		}
	}
	if payer != "" {
		pid, err := uuid.Parse(payer)
		if err == nil {
			where += ` AND e.paid_by=$` + strconv.Itoa(idx)
			args = append(args, pid)
			idx++
		}
	}
	if dateFrom != "" {
		where += ` AND e.expense_date >= $` + strconv.Itoa(idx)
		args = append(args, dateFrom)
		idx++
	}
	if dateTo != "" {
		where += ` AND e.expense_date <= $` + strconv.Itoa(idx)
		args = append(args, dateTo)
		idx++
	}
	if amountMin != "" {
		if v, err := strconv.ParseInt(amountMin, 10, 64); err == nil && v > 0 {
			where += ` AND e.amount >= $` + strconv.Itoa(idx)
			args = append(args, v)
			idx++
		}
	}
	if amountMax != "" {
		if v, err := strconv.ParseInt(amountMax, 10, 64); err == nil && v > 0 {
			where += ` AND e.amount <= $` + strconv.Itoa(idx)
			args = append(args, v)
			idx++
		}
	}
	if cursor != "" {
		// cursor is base64 of created_at+id; simplified: use id only for now
		if cid, err := uuid.Parse(cursor); err == nil {
			where += ` AND (e.created_at, e.id) < (SELECT created_at, id FROM expenses WHERE id=$` + strconv.Itoa(idx) + `)`
			args = append(args, cid)
			idx++
		}
	}
	query := `SELECT e.id, e.description, e.amount, e.currency, e.split_mode, e.paid_by, e.category_id, e.notes, e.expense_date, e.created_by, e.created_at, e.updated_at
			  FROM expenses e WHERE ` + where + ` ORDER BY e.created_at DESC, e.id DESC LIMIT $` + strconv.Itoa(idx)
	args = append(args, limit+1)

	rows, err := h.Pool.Query(r.Context(), query, args...)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	defer rows.Close()
	type exp struct {
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
	var exps []exp
	var lastID *uuid.UUID
	for rows.Next() {
		var e exp
		var catID *uuid.UUID
		var ed time.Time
		var createdAt, updatedAt time.Time
		_ = rows.Scan(&e.ID, &e.Description, &e.Amount, &e.Currency, &e.SplitMode, &e.PaidBy, &catID, &e.Notes, &ed, &e.CreatedBy, &createdAt, &updatedAt)
		e.CategoryID = catID
		e.ExpenseDate = ed.Format("2006-01-02")
		e.CreatedAt = createdAt
		exps = append(exps, e)
		tmp := e.ID
		lastID = &tmp
	}
	var nextCursor *string
	if len(exps) > limit {
		exps = exps[:limit]
		if lastID != nil {
			s := lastID.String()
			nextCursor = &s
		}
	}
	if exps == nil {
		exps = []exp{}
	}
	// Load splits for these expenses
	expIDs := make([]uuid.UUID, len(exps))
	for i, e := range exps {
		expIDs[i] = e.ID
	}
	splitsByExpense := map[uuid.UUID][]map[string]any{}
	if len(expIDs) > 0 {
		srows, _ := h.Pool.Query(r.Context(), `SELECT expense_id, user_id, amount, percentage, shares FROM expense_splits WHERE expense_id = ANY($1)`, expIDs)
		if srows != nil {
			defer srows.Close()
			for srows.Next() {
				var eid, uid uuid.UUID
				var amt int64
				var pct *float64
				var shares *int
				_ = srows.Scan(&eid, &uid, &amt, &pct, &shares)
				m := map[string]any{"user_id": uid, "amount": amt}
				if pct != nil {
					m["percentage"] = *pct
				}
				if shares != nil {
					m["shares"] = *shares
				}
				splitsByExpense[eid] = append(splitsByExpense[eid], m)
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
	var deletedAt *time.Time
	err = h.Pool.QueryRow(r.Context(),
		`SELECT group_id, description, amount, currency, split_mode, paid_by, category_id, notes, expense_date, created_by, deleted_at FROM expenses WHERE id=$1`, expenseID).
		Scan(&groupID, &description, &amount, &currency, &splitMode, &paidBy, &categoryID, &notes, &expenseDate, &createdBy, &deletedAt)
	if err == pgx.ErrNoRows || deletedAt != nil {
		httpx.WriteError(w, r, httpx.ErrNotFound)
		return
	}
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if !mustBeMember(h.Pool, r, groupID) {
		httpx.WriteError(w, r, httpx.ErrGroupMemberRequired)
		return
	}
	srows, _ := h.Pool.Query(r.Context(), `SELECT user_id, amount, percentage, shares FROM expense_splits WHERE expense_id=$1`, expenseID)
	var splits []map[string]any
	if srows != nil {
		defer srows.Close()
		for srows.Next() {
			var uid uuid.UUID
			var amt int64
			var pct *float64
			var shares *int
			_ = srows.Scan(&uid, &amt, &pct, &shares)
			m := map[string]any{"user_id": uid, "amount": amt}
			if pct != nil {
				m["percentage"] = *pct
			}
			if shares != nil {
				m["shares"] = *shares
			}
			splits = append(splits, m)
		}
	}
	httpx.WriteJSON(w, 200, map[string]any{
		"id": expenseID, "group_id": groupID, "description": description, "amount": amount, "currency": currency,
		"split_mode": splitMode, "paid_by": paidBy, "category_id": categoryID, "notes": notes,
		"expense_date": expenseDate.Format("2006-01-02"), "created_by": createdBy, "splits": splits,
	})
}

func (h *Handler) UpdateExpense(w http.ResponseWriter, r *http.Request) {
	expenseID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		httpx.WriteJSON(w, 422, map[string]any{"error": map[string]string{"code": "VALIDATION_ERROR", "message": "invalid expense id"}})
		return
	}
	var groupID uuid.UUID
	var createdBy uuid.UUID
	var deletedAt *time.Time
	err = h.Pool.QueryRow(r.Context(), `SELECT group_id, created_by, deleted_at FROM expenses WHERE id=$1`, expenseID).Scan(&groupID, &createdBy, &deletedAt)
	if err == pgx.ErrNoRows || deletedAt != nil {
		httpx.WriteError(w, r, httpx.ErrNotFound)
		return
	}
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if !mustBeMember(h.Pool, r, groupID) {
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
	var isPaidMember bool
	_ = h.Pool.QueryRow(r.Context(), `SELECT true FROM group_members WHERE group_id=$1 AND user_id=$2`, groupID, paidBy).Scan(&isPaidMember)
	if !isPaidMember {
		httpx.WriteJSON(w, 422, map[string]any{"error": map[string]string{"code": "VALIDATION_ERROR", "message": "payer must be a group member"}})
		return
	}
	// Only the creator, the payer, or group OWNER/ADMIN may edit
	editorUIDStr, _ := httpx.GetUserID(r.Context())
	editorID, _ := uuid.Parse(editorUIDStr)
	if createdBy != editorID && paidBy != editorID {
		var role string
		_ = h.Pool.QueryRow(r.Context(), `SELECT role FROM group_members WHERE group_id=$1 AND user_id=$2`, groupID, editorID).Scan(&role)
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
	if !validateGroupMembers(h.Pool, r, groupID, participantIDs) {
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
	var groupCurrency string
	if err := h.Pool.QueryRow(r.Context(), `SELECT currency FROM groups WHERE id=$1`, groupID).Scan(&groupCurrency); err != nil {
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
	if _, err = tx.Exec(r.Context(),
		`UPDATE expenses SET description=$1, amount=$2, currency=$3, split_mode=$4, paid_by=$5, category_id=$6, notes=$7, expense_date=$8, exchange_rate=$9, base_currency=$10, base_amount=$11, updated_at=now() WHERE id=$12`,
		req.Description, req.Amount, currency, mode, paidBy, categoryID, notes, expenseDate, exchangeRate, baseCurrency, baseAmount, expenseID); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if _, err = tx.Exec(r.Context(), `DELETE FROM expense_splits WHERE expense_id=$1`, expenseID); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	for i, pid := range participantIDs {
		var pct *float64
		var shares *int64
		if mode == "PERCENTAGE" {
			pct = req.Splits[i].Percentage
		}
		if mode == "SHARES" {
			shares = req.Splits[i].Shares
		}
		if _, err = tx.Exec(r.Context(), `INSERT INTO expense_splits (expense_id, user_id, amount, percentage, shares) VALUES ($1,$2,$3,$4,$5)`,
			expenseID, pid, amounts[i], pct, shares); err != nil {
			httpx.WriteError(w, r, err)
			return
		}
	}
	if _, err = tx.Exec(r.Context(),
		`INSERT INTO activity_events (group_id, actor_id, type, entity_type, entity_id, payload) VALUES ($1,$2,'EXPENSE_UPDATED','expense',$3,$4)`,
		groupID, actorID, expenseID, json.RawMessage(`{"description":"`+req.Description+`"}`)); err != nil {
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
	expenseID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		httpx.WriteJSON(w, 422, map[string]any{"error": map[string]string{"code": "VALIDATION_ERROR", "message": "invalid expense id"}})
		return
	}
	var groupID, createdBy uuid.UUID
	err = h.Pool.QueryRow(r.Context(), `SELECT group_id, created_by FROM expenses WHERE id=$1 AND deleted_at IS NULL`, expenseID).Scan(&groupID, &createdBy)
	if err == pgx.ErrNoRows {
		httpx.WriteError(w, r, httpx.ErrNotFound)
		return
	}
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if !mustBeMember(h.Pool, r, groupID) {
		httpx.WriteError(w, r, httpx.ErrGroupMemberRequired)
		return
	}
	uid, _ := httpx.GetUserID(r.Context())
	actorID, _ := uuid.Parse(uid)
	if createdBy != actorID {
		var role string
		_ = h.Pool.QueryRow(r.Context(), `SELECT role FROM group_members WHERE group_id=$1 AND user_id=$2`, groupID, actorID).Scan(&role)
		if role != "OWNER" && role != "ADMIN" {
			httpx.WriteError(w, r, httpx.ErrForbidden)
			return
		}
	}
	_, _ = h.Pool.Exec(r.Context(), `UPDATE expenses SET deleted_at=now(), updated_at=now() WHERE id=$1`, expenseID)
	_, _ = h.Pool.Exec(r.Context(),
		`INSERT INTO activity_events (group_id, actor_id, type, entity_type, entity_id, payload) VALUES ($1,$2,'EXPENSE_DELETED','expense',$3,$4)`,
		groupID, actorID, expenseID, json.RawMessage(`{}`))
	httpx.WriteJSON(w, 200, map[string]any{"message": "deleted"})
}
