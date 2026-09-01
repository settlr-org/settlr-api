package settlements

import (
	"context"
	"encoding/json"
	"net/http"
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
)

// Handler handles settlement-related HTTP requests.
// Pool is the raw pgx pool for transaction fallback; Queries is the sqlc-generated wrapper.
// See internal/db/queries/settlements.sql for migrated queries.
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
	mux.Handle("POST /api/v1/groups/{id}/settlements", authMw(http.HandlerFunc(h.CreateSettlement)))
	mux.Handle("GET /api/v1/groups/{id}/settlements", authMw(http.HandlerFunc(h.ListSettlements)))
	mux.Handle("PATCH /api/v1/settlements/{id}", authMw(http.HandlerFunc(h.UpdateSettlement)))
	mux.Handle("DELETE /api/v1/settlements/{id}", authMw(http.HandlerFunc(h.DeleteSettlement)))
}

func (h *Handler) isMember(ctx context.Context, groupID, userID uuid.UUID) bool {
	ok, err := h.Queries.CheckSettlementIsGroupMember(ctx, db.CheckSettlementIsGroupMemberParams{
		GroupID: groupID,
		UserID:  userID,
	})
	if err == nil {
		return ok
	}
	return false
}

func (h *Handler) mustBeMember(r *http.Request, groupID uuid.UUID) bool {
	h.ensureQueries()
	uid, _ := httpx.GetUserID(r.Context())
	userID, _ := uuid.Parse(uid)
	if h.Queries != nil {
		return h.isMember(r.Context(), groupID, userID)
	}
	// Fallback should not happen after sqlc migration; use sqlc via pool if needed.
	if h.Pool != nil {
		q := db.New(h.Pool)
		ok, _ := q.CheckSettlementIsGroupMember(r.Context(), db.CheckSettlementIsGroupMemberParams{GroupID: groupID, UserID: userID})
		return ok
	}
	return false
}

// mustBeMember legacy helper kept for compatibility; now uses sqlc instead of manual Pool query.
func mustBeMember(pool *pgxpool.Pool, r *http.Request, groupID uuid.UUID) bool {
	if pool == nil {
		return false
	}
	q := db.New(pool)
	uid, _ := httpx.GetUserID(r.Context())
	userID, _ := uuid.Parse(uid)
	ok, _ := q.CheckSettlementIsGroupMember(r.Context(), db.CheckSettlementIsGroupMemberParams{GroupID: groupID, UserID: userID})
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
	h.ensureQueries()
	groupID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		httpx.WriteJSON(w, 422, map[string]any{"error": map[string]string{"code": "VALIDATION_ERROR", "message": "invalid group id"}})
		return
	}
	if !h.mustBeMember(r, groupID) {
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
	fOk := h.isMember(r.Context(), groupID, from)
	tOk := h.isMember(r.Context(), groupID, to)
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
		if cur, err := h.Queries.GetSettlementGroupCurrency(r.Context(), groupID); err == nil && cur != "" {
			currency = cur
		}
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
	err = h.Queries.CreateSettlement(r.Context(), db.CreateSettlementParams{
		ID:        id,
		GroupID:   groupID,
		FromUser:  from,
		ToUser:    to,
		Amount:    req.Amount,
		Currency:  currency,
		Note:      note,
		SettledAt: pgtype.Timestamptz{Time: settledAt, Valid: true},
		CreatedBy: createdBy,
	})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	_ = h.Queries.CreateSettlementActivity(r.Context(), db.CreateSettlementActivityParams{
		GroupID:  groupID,
		ActorID:  createdBy,
		EntityID: id,
		Payload:  json.RawMessage(`{"amount":` + strconv.FormatInt(req.Amount, 10) + `}`),
	})
	// Notify participants
	for _, nid := range []uuid.UUID{from, to} {
		if nid == createdBy {
			continue
		}
		_ = h.Queries.CreateSettlementNotification(r.Context(), db.CreateSettlementNotificationParams{
			UserID: nid,
			Title:  "Settlement recorded",
			Body:   note,
			Data:   json.RawMessage(`{"group_id":"` + groupID.String() + `"}`),
		})
	}
	httpx.WriteJSON(w, 201, map[string]any{"id": id, "group_id": groupID, "from_user": from, "to_user": to, "amount": req.Amount, "currency": currency})
}

func (h *Handler) ListSettlements(w http.ResponseWriter, r *http.Request) {
	h.ensureQueries()
	groupID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		httpx.WriteJSON(w, 422, map[string]any{"error": map[string]string{"code": "VALIDATION_ERROR", "message": "invalid group id"}})
		return
	}
	if !h.mustBeMember(r, groupID) {
		httpx.WriteError(w, r, httpx.ErrGroupMemberRequired)
		return
	}
	limit := 30
	if v, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && v > 0 && v <= 100 {
		limit = v
	}
	rows, err := h.Queries.ListSettlements(r.Context(), db.ListSettlementsParams{
		GroupID: groupID,
		Limit:   int32(limit + 1),
	})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	var out []map[string]any
	for _, row := range rows {
		out = append(out, map[string]any{"id": row.ID, "group_id": groupID, "from_user": row.FromUser, "to_user": row.ToUser, "amount": row.Amount, "currency": row.Currency, "note": row.Note, "settled_at": row.SettledAt.Time, "created_by": row.CreatedBy, "created_at": row.CreatedAt.Time})
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
	h.ensureQueries()
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		httpx.WriteJSON(w, 422, map[string]any{"error": map[string]string{"code": "VALIDATION_ERROR", "message": "invalid settlement id"}})
		return
	}
	row, err := h.Queries.GetSettlementForUpdate(r.Context(), id)
	if err == pgx.ErrNoRows || (err == nil && row.DeletedAt.Valid) {
		httpx.WriteError(w, r, httpx.ErrNotFound)
		return
	}
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	groupID := row.GroupID
	if !h.mustBeMember(r, groupID) {
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
		_ = h.Queries.UpdateSettlementAmount(r.Context(), db.UpdateSettlementAmountParams{
			Amount: *req.Amount,
			ID:     id,
		})
	}
	if req.Note != nil {
		_ = h.Queries.UpdateSettlementNote(r.Context(), db.UpdateSettlementNoteParams{
			Note: *req.Note,
			ID:   id,
		})
	}
	httpx.WriteJSON(w, 200, map[string]any{"message": "updated"})
}

func (h *Handler) DeleteSettlement(w http.ResponseWriter, r *http.Request) {
	h.ensureQueries()
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		httpx.WriteJSON(w, 422, map[string]any{"error": map[string]string{"code": "VALIDATION_ERROR", "message": "invalid settlement id"}})
		return
	}
	groupID, err := h.Queries.GetSettlementForDelete(r.Context(), id)
	if err == pgx.ErrNoRows {
		httpx.WriteError(w, r, httpx.ErrNotFound)
		return
	}
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if !h.mustBeMember(r, groupID) {
		httpx.WriteError(w, r, httpx.ErrGroupMemberRequired)
		return
	}
	_ = h.Queries.SoftDeleteSettlement(r.Context(), id)
	httpx.WriteJSON(w, 200, map[string]any{"message": "deleted"})
}

// Ensure imports are used
var _ = pgtype.Timestamptz{}
