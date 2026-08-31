package settlements

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/settlr-org/settlr-api/internal/auth"
	"github.com/settlr-org/settlr-api/internal/httpx"
)

type Handler struct {
	Pool *pgxpool.Pool
}

func (h *Handler) RegisterRoutes(mux *http.ServeMux, authMw func(http.Handler) http.Handler) {
	mux.Handle("POST /api/v1/groups/{id}/settlements", authMw(http.HandlerFunc(h.CreateSettlement)))
	mux.Handle("GET /api/v1/groups/{id}/settlements", authMw(http.HandlerFunc(h.ListSettlements)))
	mux.Handle("PATCH /api/v1/settlements/{id}", authMw(http.HandlerFunc(h.UpdateSettlement)))
	mux.Handle("DELETE /api/v1/settlements/{id}", authMw(http.HandlerFunc(h.DeleteSettlement)))
}

func mustBeMember(pool *pgxpool.Pool, r *http.Request, groupID uuid.UUID) bool {
	uid, _ := httpx.GetUserID(r.Context())
	userID, _ := uuid.Parse(uid)
	var ok bool
	_ = pool.QueryRow(r.Context(), `SELECT true FROM group_members WHERE group_id=$1 AND user_id=$2`, groupID, userID).Scan(&ok)
	return ok
}

type createReq struct {
	FromUser  string  `json:"from_user"`
	ToUser    string  `json:"to_user"`
	Amount    int64   `json:"amount"`
	Currency  *string `json:"currency"`
	Note      *string `json:"note"`
	SettledAt *string `json:"settled_at"`
}

func (h *Handler) CreateSettlement(w http.ResponseWriter, r *http.Request) {
	groupID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		httpx.WriteJSON(w, 422, map[string]any{"error": map[string]string{"code": "VALIDATION_ERROR", "message": "invalid group id"}})
		return
	}
	if !mustBeMember(h.Pool, r, groupID) {
		httpx.WriteError(w, r, httpx.ErrGroupMemberRequired)
		return
	}
	var req createReq
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteJSON(w, 400, map[string]any{"error": map[string]string{"code": "VALIDATION_ERROR", "message": err.Error()}})
		return
	}
	from, err := uuid.Parse(req.FromUser)
	if err != nil {
		httpx.WriteJSON(w, 422, map[string]any{"error": map[string]string{"code": "VALIDATION_ERROR", "message": "invalid from_user"}})
		return
	}
	to, err := uuid.Parse(req.ToUser)
	if err != nil {
		httpx.WriteJSON(w, 422, map[string]any{"error": map[string]string{"code": "VALIDATION_ERROR", "message": "invalid to_user"}})
		return
	}
	if from == to {
		httpx.WriteJSON(w, 422, map[string]any{"error": map[string]string{"code": "VALIDATION_ERROR", "message": "from_user and to_user must differ"}})
		return
	}
	if req.Amount <= 0 {
		httpx.WriteJSON(w, 422, map[string]any{"error": map[string]string{"code": "VALIDATION_ERROR", "message": "amount must be > 0"}})
		return
	}
	// Both must be members
	var fOk, tOk bool
	_ = h.Pool.QueryRow(r.Context(), `SELECT true FROM group_members WHERE group_id=$1 AND user_id=$2`, groupID, from).Scan(&fOk)
	_ = h.Pool.QueryRow(r.Context(), `SELECT true FROM group_members WHERE group_id=$1 AND user_id=$2`, groupID, to).Scan(&tOk)
	if !fOk || !tOk {
		httpx.WriteJSON(w, 422, map[string]any{"error": map[string]string{"code": "VALIDATION_ERROR", "message": "both users must be group members"}})
		return
	}
	currency := "NPR"
	if req.Currency != nil {
		currency = strings.ToUpper(*req.Currency)
		if !auth.SupportedCurrencies[currency] {
			httpx.WriteJSON(w, 422, map[string]any{"error": map[string]string{"code": "VALIDATION_ERROR", "message": "unsupported currency"}})
			return
		}
	} else {
		_ = h.Pool.QueryRow(r.Context(), `SELECT currency FROM groups WHERE id=$1`, groupID).Scan(&currency)
	}
	note := ""
	if req.Note != nil {
		note = *req.Note
	}
	settledAt := time.Now().UTC()
	if req.SettledAt != nil && *req.SettledAt != "" {
		if t, err := time.Parse(time.RFC3339, *req.SettledAt); err == nil {
			settledAt = t
		} else if t, err := time.Parse("2006-01-02", *req.SettledAt); err == nil {
			settledAt = t
		}
	}
	uid, _ := httpx.GetUserID(r.Context())
	createdBy, _ := uuid.Parse(uid)
	id := uuid.New()
	_, err = h.Pool.Exec(r.Context(),
		`INSERT INTO settlements (id, group_id, from_user, to_user, amount, currency, note, settled_at, created_by)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
		id, groupID, from, to, req.Amount, currency, note, settledAt, createdBy)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	_, _ = h.Pool.Exec(r.Context(),
		`INSERT INTO activity_events (group_id, actor_id, type, entity_type, entity_id, payload) VALUES ($1,$2,'SETTLEMENT_RECORDED','settlement',$3,$4)`,
		groupID, createdBy, id, json.RawMessage(`{"amount":`+strconv.FormatInt(req.Amount, 10)+`}`))
	// Notify participants
	for _, nid := range []uuid.UUID{from, to} {
		if nid == createdBy {
			continue
		}
		_, _ = h.Pool.Exec(r.Context(),
			`INSERT INTO notifications (user_id, type, title, body, data) VALUES ($1,'SETTLEMENT_RECORDED',$2,$3,$4)`,
			nid, "Settlement recorded", note, json.RawMessage(`{"group_id":"`+groupID.String()+`"}`))
	}
	httpx.WriteJSON(w, 201, map[string]any{"id": id, "group_id": groupID, "from_user": from, "to_user": to, "amount": req.Amount, "currency": currency})
}

func (h *Handler) ListSettlements(w http.ResponseWriter, r *http.Request) {
	groupID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		httpx.WriteJSON(w, 422, map[string]any{"error": map[string]string{"code": "VALIDATION_ERROR", "message": "invalid group id"}})
		return
	}
	if !mustBeMember(h.Pool, r, groupID) {
		httpx.WriteError(w, r, httpx.ErrGroupMemberRequired)
		return
	}
	limit := 30
	if v, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && v > 0 && v <= 100 {
		limit = v
	}
	rows, err := h.Pool.Query(r.Context(),
		`SELECT id, from_user, to_user, amount, currency, note, settled_at, created_by, created_at
		 FROM settlements WHERE group_id=$1 AND deleted_at IS NULL ORDER BY settled_at DESC, created_at DESC LIMIT $2`, groupID, limit+1)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	defer rows.Close()
	var out []map[string]any
	for rows.Next() {
		var id, from, to, createdBy uuid.UUID
		var amount int64
		var currency, note string
		var settledAt, createdAt time.Time
		_ = rows.Scan(&id, &from, &to, &amount, &currency, &note, &settledAt, &createdBy, &createdAt)
		out = append(out, map[string]any{"id": id, "group_id": groupID, "from_user": from, "to_user": to, "amount": amount, "currency": currency, "note": note, "settled_at": settledAt, "created_by": createdBy, "created_at": createdAt})
	}
	if len(out) > limit {
		out = out[:limit]
		httpx.WriteJSON(w, 200, map[string]any{"data": out, "next_cursor": out[len(out)-1]["id"]})
		return
	}
	if out == nil {
		out = []map[string]any{}
	}
	httpx.WriteJSON(w, 200, map[string]any{"data": out})
}

func (h *Handler) UpdateSettlement(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		httpx.WriteJSON(w, 422, map[string]any{"error": map[string]string{"code": "VALIDATION_ERROR", "message": "invalid settlement id"}})
		return
	}
	var groupID uuid.UUID
	var deletedAt *time.Time
	err = h.Pool.QueryRow(r.Context(), `SELECT group_id, deleted_at FROM settlements WHERE id=$1`, id).Scan(&groupID, &deletedAt)
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
	var req struct {
		Amount *int64  `json:"amount"`
		Note   *string `json:"note"`
	}
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteJSON(w, 400, map[string]any{"error": map[string]string{"code": "VALIDATION_ERROR", "message": err.Error()}})
		return
	}
	if req.Amount != nil {
		if *req.Amount <= 0 {
			httpx.WriteJSON(w, 422, map[string]any{"error": map[string]string{"code": "VALIDATION_ERROR", "message": "amount must be > 0"}})
			return
		}
		_, _ = h.Pool.Exec(r.Context(), `UPDATE settlements SET amount=$1, updated_at=now() WHERE id=$2`, *req.Amount, id)
	}
	if req.Note != nil {
		_, _ = h.Pool.Exec(r.Context(), `UPDATE settlements SET note=$1, updated_at=now() WHERE id=$2`, *req.Note, id)
	}
	httpx.WriteJSON(w, 200, map[string]any{"message": "updated"})
}

func (h *Handler) DeleteSettlement(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		httpx.WriteJSON(w, 422, map[string]any{"error": map[string]string{"code": "VALIDATION_ERROR", "message": "invalid settlement id"}})
		return
	}
	var groupID uuid.UUID
	err = h.Pool.QueryRow(r.Context(), `SELECT group_id FROM settlements WHERE id=$1 AND deleted_at IS NULL`, id).Scan(&groupID)
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
	_, _ = h.Pool.Exec(r.Context(), `UPDATE settlements SET deleted_at=now(), updated_at=now() WHERE id=$1`, id)
	httpx.WriteJSON(w, 200, map[string]any{"message": "deleted"})
}
