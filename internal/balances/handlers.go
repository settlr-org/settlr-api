package balances

import (
	"net/http"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nabinkhanal00/settlr-api/internal/debts"
	"github.com/nabinkhanal00/settlr-api/internal/httpx"
)

type Handler struct {
	Pool *pgxpool.Pool
}

func (h *Handler) RegisterRoutes(mux *http.ServeMux, authMw func(http.Handler) http.Handler) {
	mux.Handle("GET /api/v1/groups/{id}/balances", authMw(http.HandlerFunc(h.GetBalances)))
	mux.Handle("GET /api/v1/groups/{id}/debts", authMw(http.HandlerFunc(h.GetDebts)))
	mux.Handle("GET /api/v1/me/balances", authMw(http.HandlerFunc(h.GetMyBalances)))
}

func mustBeMember(pool *pgxpool.Pool, r *http.Request, groupID uuid.UUID) bool {
	uid, _ := httpx.GetUserID(r.Context())
	userID, _ := uuid.Parse(uid)
	var ok bool
	_ = pool.QueryRow(r.Context(), `SELECT true FROM group_members WHERE group_id=$1 AND user_id=$2`, groupID, userID).Scan(&ok)
	return ok
}

// GetBalances computes per-member net balances for a group.
// balance = total_paid - total_owed + settlements_received - settlements_paid (via settlements logic: from gets +amount, to gets -amount)
func (h *Handler) GetBalances(w http.ResponseWriter, r *http.Request) {
	groupID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		httpx.WriteJSON(w, 422, map[string]any{"error": map[string]string{"code": "VALIDATION_ERROR", "message": "invalid group id"}})
		return
	}
	if !mustBeMember(h.Pool, r, groupID) {
		httpx.WriteError(w, r, httpx.ErrGroupMemberRequired)
		return
	}
	balMap, err := h.computeBalances(r, groupID)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	currency := "NPR"
	_ = h.Pool.QueryRow(r.Context(), `SELECT currency FROM groups WHERE id=$1`, groupID).Scan(&currency)
	var out []map[string]any
	for uid, amt := range balMap {
		out = append(out, map[string]any{"user_id": uid, "amount": amt})
	}
	if out == nil {
		out = []map[string]any{}
	}
	httpx.WriteJSON(w, 200, map[string]any{"data": out, "currency": currency})
}

func (h *Handler) GetDebts(w http.ResponseWriter, r *http.Request) {
	groupID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		httpx.WriteJSON(w, 422, map[string]any{"error": map[string]string{"code": "VALIDATION_ERROR", "message": "invalid group id"}})
		return
	}
	if !mustBeMember(h.Pool, r, groupID) {
		httpx.WriteError(w, r, httpx.ErrGroupMemberRequired)
		return
	}
	balMap, err := h.computeBalances(r, groupID)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	var bals []debts.Balance
	for uid, amt := range balMap {
		bals = append(bals, debts.Balance{UserID: uid, Amount: amt})
	}
	simplified := debts.Simplify(bals)
	currency := "NPR"
	_ = h.Pool.QueryRow(r.Context(), `SELECT currency FROM groups WHERE id=$1`, groupID).Scan(&currency)
	httpx.WriteJSON(w, 200, map[string]any{"data": simplified, "currency": currency})
}

func (h *Handler) GetMyBalances(w http.ResponseWriter, r *http.Request) {
	uid, _ := httpx.GetUserID(r.Context())
	userID, _ := uuid.Parse(uid)
	var userCurrency string
	_ = h.Pool.QueryRow(r.Context(), `SELECT default_currency FROM users WHERE id=$1`, userID).Scan(&userCurrency)
	if userCurrency == "" {
		userCurrency = "NPR"
	}
	// Aggregate across all groups the user is in
	rows, err := h.Pool.Query(r.Context(), `
		SELECT g.id, g.name, g.currency,
		       COALESCE(paid.total,0) - COALESCE(owed.total,0) + COALESCE(recv.total,0) - COALESCE(sent.total,0) AS balance
		FROM groups g
		JOIN group_members gm ON gm.group_id=g.id AND gm.user_id=$1
		LEFT JOIN (
			SELECT group_id, paid_by, SUM(ROUND(amount * COALESCE(exchange_rate, 1))::bigint) AS total FROM expenses WHERE deleted_at IS NULL GROUP BY group_id, paid_by
		) paid ON paid.group_id=g.id AND paid.paid_by=$1
		LEFT JOIN (
			SELECT e.group_id, s.user_id, SUM(ROUND(s.amount * COALESCE(e.exchange_rate, 1))::bigint) AS total
			FROM expense_splits s JOIN expenses e ON e.id=s.expense_id
			WHERE e.deleted_at IS NULL GROUP BY e.group_id, s.user_id
		) owed ON owed.group_id=g.id AND owed.user_id=$1
		LEFT JOIN (
			SELECT group_id, from_user AS user_id, SUM(amount) AS total FROM settlements WHERE deleted_at IS NULL GROUP BY group_id, from_user
		) recv ON recv.group_id=g.id AND recv.user_id=$1
		LEFT JOIN (
			SELECT group_id, to_user AS user_id, SUM(amount) AS total FROM settlements WHERE deleted_at IS NULL GROUP BY group_id, to_user
		) sent ON sent.group_id=g.id AND sent.user_id=$1
		WHERE g.archived_at IS NULL
	`, userID)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	defer rows.Close()
	var totalOwed, totalOwing int64
	var data []map[string]any
	for rows.Next() {
		var gid uuid.UUID
		var name, currency string
		var bal int64
		_ = rows.Scan(&gid, &name, &currency, &bal)
		if currency == "" {
			currency = "NPR"
		}
		data = append(data, map[string]any{"group_id": gid, "group_name": name, "currency": currency, "balance": bal})
		if bal > 0 {
			totalOwed += bal
		} else {
			totalOwing += -bal
		}
	}
	if data == nil {
		data = []map[string]any{}
	}
	net := totalOwed - totalOwing
	httpx.WriteJSON(w, 200, map[string]any{
		"data":     data,
		"currency": userCurrency,
		"summary": map[string]any{
			"you_are_owed": totalOwed,
			"you_owe":      totalOwing,
			"net_balance":  net,
		},
	})
}

func (h *Handler) computeBalances(r *http.Request, groupID uuid.UUID) (map[uuid.UUID]int64, error) {
	// Fetch group members to include zero-balance members
	rows, err := h.Pool.Query(r.Context(), `SELECT user_id FROM group_members WHERE group_id=$1`, groupID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	m := map[uuid.UUID]int64{}
	for rows.Next() {
		var uid uuid.UUID
		_ = rows.Scan(&uid)
		m[uid] = 0
	}
	// Aggregate expenses: paid and splits
	// Paid
	paidRows, _ := h.Pool.Query(r.Context(),
		`SELECT paid_by, SUM(ROUND(amount * COALESCE(exchange_rate, 1))::bigint) FROM expenses WHERE group_id=$1 AND deleted_at IS NULL GROUP BY paid_by`, groupID)
	if paidRows != nil {
		defer paidRows.Close()
		for paidRows.Next() {
			var uid uuid.UUID
			var s int64
			_ = paidRows.Scan(&uid, &s)
			m[uid] += s
		}
	}
	// Owed (splits)
	owedRows, _ := h.Pool.Query(r.Context(),
		`SELECT s.user_id, SUM(ROUND(s.amount * COALESCE(e.exchange_rate, 1))::bigint) FROM expense_splits s
		 JOIN expenses e ON e.id=s.expense_id
		 WHERE e.group_id=$1 AND e.deleted_at IS NULL GROUP BY s.user_id`, groupID)
	if owedRows != nil {
		defer owedRows.Close()
		for owedRows.Next() {
			var uid uuid.UUID
			var s int64
			_ = owedRows.Scan(&uid, &s)
			m[uid] -= s
		}
	}
	// Settlements: from gets +amount (they paid), to gets -amount
	settleRows, _ := h.Pool.Query(r.Context(),
		`SELECT from_user, to_user, amount FROM settlements WHERE group_id=$1 AND deleted_at IS NULL`, groupID)
	if settleRows != nil {
		defer settleRows.Close()
		for settleRows.Next() {
			var from, to uuid.UUID
			var amt int64
			_ = settleRows.Scan(&from, &to, &amt)
			m[from] += amt
			m[to] -= amt
		}
	}
	return m, nil
}
