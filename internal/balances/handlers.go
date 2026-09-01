package balances

import (
	"net/http"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	db "github.com/settlr-org/settlr-api/internal/db"
	"github.com/settlr-org/settlr-api/internal/debts"
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
	mux.Handle("GET /api/v1/groups/{id}/balances", authMw(http.HandlerFunc(h.GetBalances)))
	mux.Handle("GET /api/v1/groups/{id}/debts", authMw(http.HandlerFunc(h.GetDebts)))
	mux.Handle("GET /api/v1/me/balances", authMw(http.HandlerFunc(h.GetMyBalances)))
}

func (h *Handler) isMember(r *http.Request, groupID uuid.UUID) bool {
	uid, _ := httpx.GetUserID(r.Context())
	userID, _ := uuid.Parse(uid)
	q := h.ensureQueries()
	if role, err := q.IsMember(r.Context(), db.IsMemberParams{GroupID: groupID, UserID: userID}); err == nil && role != "" {
		return true
	}
	return false
}

// GetBalances computes per-member net balances for a group.
// balance = total_paid - total_owed + settlements_received - settlements_paid (via settlements logic: from gets +amount, to gets -amount)
func (h *Handler) GetBalances(w http.ResponseWriter, r *http.Request) {
	groupID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		httpx.WriteJSON(w, 422, map[string]any{"error": map[string]string{"code": "VALIDATION_ERROR", "message": "invalid group id"}})
		return
	}
	if !h.isMember(r, groupID) {
		httpx.WriteError(w, r, httpx.ErrGroupMemberRequired)
		return
	}
	balMap, err := h.computeBalances(r, groupID)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	currency := "NPR"
	q := h.ensureQueries()
	if c, err := q.GetGroupCurrency(r.Context(), groupID); err == nil {
		currency = c
	}
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
	if !h.isMember(r, groupID) {
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
	q := h.ensureQueries()
	if c, err := q.GetGroupCurrency(r.Context(), groupID); err == nil {
		currency = c
	}
	httpx.WriteJSON(w, 200, map[string]any{"data": simplified, "currency": currency})
}

func (h *Handler) GetMyBalances(w http.ResponseWriter, r *http.Request) {
	uid, _ := httpx.GetUserID(r.Context())
	userID, _ := uuid.Parse(uid)
	q := h.ensureQueries()
	var userCurrency string
	if c, err := q.GetUserDefaultCurrency(r.Context(), userID); err == nil && c != "" {
		userCurrency = c
	}
	if userCurrency == "" {
		userCurrency = "NPR"
	}
	rows, err := q.GetMyGroupBalances(r.Context(), userID)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	var totalOwed, totalOwing int64
	var data []map[string]any
	for _, row := range rows {
		currency := row.Currency
		if currency == "" {
			currency = "NPR"
		}
		bal := int64(row.Balance)
		data = append(data, map[string]any{"group_id": row.ID, "group_name": row.Name, "currency": currency, "balance": bal})
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
	q := h.ensureQueries()
	m := map[uuid.UUID]int64{}
	memberIDs, err := q.ListGroupMemberIDs(r.Context(), groupID)
	if err != nil {
		return nil, err
	}
	for _, uid := range memberIDs {
		m[uid] = 0
	}
	paidRows, _ := q.SumPaidByGroup(r.Context(), groupID)
	for _, row := range paidRows {
		m[row.UserID] += row.Total
	}
	owedRows, _ := q.SumOwedByGroup(r.Context(), groupID)
	for _, row := range owedRows {
		m[row.UserID] -= row.Total
	}
	settleRows, _ := q.ListSettlementsByGroup(r.Context(), groupID)
	for _, row := range settleRows {
		m[row.FromUser] += row.Amount
		m[row.ToUser] -= row.Amount
	}
	return m, nil
}
