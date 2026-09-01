package recurring

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	db "github.com/settlr-org/settlr-api/internal/db"
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

func (h *Handler) isMember(ctx context.Context, groupID, userID uuid.UUID) bool {
	q := h.ensureQueries()
	if q == nil {
		return false
	}
	role, err := q.IsMember(ctx, db.IsMemberParams{GroupID: groupID, UserID: userID})
	if err != nil {
		return false
	}
	return role != ""
}

func (h *Handler) RegisterRoutes(mux *http.ServeMux, authMw func(http.Handler) http.Handler) {
	mux.Handle("GET /api/v1/groups/{id}/recurring", authMw(http.HandlerFunc(h.List)))
	mux.Handle("POST /api/v1/groups/{id}/recurring", authMw(http.HandlerFunc(h.Create)))
	mux.Handle("PATCH /api/v1/recurring/{id}", authMw(http.HandlerFunc(h.Update)))
	mux.Handle("DELETE /api/v1/recurring/{id}", authMw(http.HandlerFunc(h.Delete)))
}

func (h *Handler) Create(w http.ResponseWriter, r *http.Request) {
	groupID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		httpx.WriteJSON(w, 422, map[string]any{"error": map[string]string{"code": "VALIDATION_ERROR", "message": "invalid group id"}})
		return
	}
	uid, _ := httpx.GetUserID(r.Context())
	userID, _ := uuid.Parse(uid)
	q := h.ensureQueries()
	if q == nil {
		httpx.WriteError(w, r, httpx.ErrGroupMemberRequired)
		return
	}
	if role, err := q.IsMember(r.Context(), db.IsMemberParams{GroupID: groupID, UserID: userID}); err != nil || role == "" {
		httpx.WriteError(w, r, httpx.ErrGroupMemberRequired)
		return
	}
	var req struct {
		Description string           `json:"description"`
		Amount      int64            `json:"amount"`
		Currency    string           `json:"currency"`
		CategoryID  *uuid.UUID       `json:"category_id"`
		SplitMode   string           `json:"split_mode"`
		Splits      []map[string]any `json:"splits"`
		PaidBy      uuid.UUID        `json:"paid_by"`
		Frequency   string           `json:"frequency"`
		StartDate   *string          `json:"start_date"`
	}
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteJSON(w, 400, map[string]any{"error": map[string]string{"code": "VALIDATION_ERROR", "message": err.Error()}})
		return
	}
	if req.Description == "" || req.Amount <= 0 {
		httpx.WriteJSON(w, 422, map[string]any{"error": map[string]string{"code": "VALIDATION_ERROR", "message": "description and positive amount required"}})
		return
	}
	if req.Frequency != "DAILY" && req.Frequency != "WEEKLY" && req.Frequency != "MONTHLY" && req.Frequency != "YEARLY" {
		httpx.WriteJSON(w, 422, map[string]any{"error": map[string]string{"code": "VALIDATION_ERROR", "message": "frequency must be DAILY, WEEKLY, MONTHLY or YEARLY"}})
		return
	}
	if req.SplitMode == "" {
		req.SplitMode = "EQUAL"
	}
	currency := req.Currency
	if currency == "" {
		if g, err := q.GetGroup(r.Context(), groupID); err == nil {
			currency = g.Currency
		}
	}
	if currency == "" {
		currency = "NPR"
	}
	start := time.Now().UTC()
	if req.StartDate != nil {
		if t, err := time.Parse("2006-01-02", *req.StartDate); err == nil {
			start = t
		}
	}
	splitsJSON, _ := json.Marshal(req.Splits)
	if req.Splits == nil {
		splitsJSON = []byte("[]")
	}
	id := uuid.New()
	categoryID := uuid.Nil
	if req.CategoryID != nil {
		categoryID = *req.CategoryID
	}
	err = q.CreateRecurringExpense(r.Context(), db.CreateRecurringExpenseParams{
		ID:          id,
		GroupID:     groupID,
		CreatedBy:   userID,
		Description: req.Description,
		Amount:      req.Amount,
		Currency:    currency,
		CategoryID:  categoryID,
		SplitMode:   req.SplitMode,
		Splits:      splitsJSON,
		PaidBy:      req.PaidBy,
		Frequency:   req.Frequency,
		NextRunAt:   pgtype.Timestamptz{Time: start, Valid: true},
	})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, 201, map[string]any{"id": id, "next_run_at": start})
}

func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	groupID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		httpx.WriteJSON(w, 422, map[string]any{"error": map[string]string{"code": "VALIDATION_ERROR", "message": "invalid group id"}})
		return
	}
	uid, _ := httpx.GetUserID(r.Context())
	userID, _ := uuid.Parse(uid)
	q := h.ensureQueries()
	if q == nil {
		httpx.WriteError(w, r, httpx.ErrGroupMemberRequired)
		return
	}
	if role, err := q.IsMember(r.Context(), db.IsMemberParams{GroupID: groupID, UserID: userID}); err != nil || role == "" {
		httpx.WriteError(w, r, httpx.ErrGroupMemberRequired)
		return
	}
	rows, err := q.ListRecurringByGroup(r.Context(), groupID)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	var out []map[string]any
	for _, row := range rows {
		var nextRun, lastRun any
		if row.NextRunAt.Valid {
			nextRun = row.NextRunAt.Time
		}
		if row.LastRunAt.Valid {
			lastRun = row.LastRunAt.Time
		}
		out = append(out, map[string]any{
			"id": row.ID, "description": row.Description, "amount": row.Amount, "currency": row.Currency,
			"split_mode": row.SplitMode, "paid_by": row.PaidBy, "frequency": row.Frequency,
			"next_run_at": nextRun, "last_run_at": lastRun, "active": row.Active,
		})
	}
	if out == nil {
		out = []map[string]any{}
	}
	httpx.WriteJSON(w, 200, map[string]any{"data": out})
}

func (h *Handler) Update(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		httpx.WriteJSON(w, 422, map[string]any{"error": map[string]string{"code": "VALIDATION_ERROR", "message": "invalid id"}})
		return
	}
	uid, _ := httpx.GetUserID(r.Context())
	userID, _ := uuid.Parse(uid)
	var req struct {
		Active *bool `json:"active"`
	}
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteJSON(w, 400, map[string]any{"error": map[string]string{"code": "VALIDATION_ERROR", "message": err.Error()}})
		return
	}
	if req.Active == nil {
		httpx.WriteJSON(w, 422, map[string]any{"error": map[string]string{"code": "VALIDATION_ERROR", "message": "nothing to update"}})
		return
	}
	q := h.ensureQueries()
	if q == nil {
		httpx.WriteError(w, r, httpx.ErrGroupMemberRequired)
		return
	}
	// Only group members of the recurring expense's group may update it
	ok, err := q.CheckRecurringGroupMember(r.Context(), db.CheckRecurringGroupMemberParams{ID: id, UserID: userID})
	if err != nil || !ok {
		httpx.WriteError(w, r, httpx.ErrGroupMemberRequired)
		return
	}
	rowsAffected, err := q.UpdateRecurringActive(r.Context(), db.UpdateRecurringActiveParams{ID: id, UserID: userID, Active: *req.Active})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if rowsAffected == 0 {
		httpx.WriteError(w, r, httpx.ErrNotFound)
		return
	}
	httpx.WriteJSON(w, 200, map[string]any{"message": "updated", "active": *req.Active})
}

func (h *Handler) Delete(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		httpx.WriteJSON(w, 422, map[string]any{"error": map[string]string{"code": "VALIDATION_ERROR", "message": "invalid id"}})
		return
	}
	uid, _ := httpx.GetUserID(r.Context())
	userID, _ := uuid.Parse(uid)
	q := h.ensureQueries()
	if q == nil {
		httpx.WriteError(w, r, httpx.ErrNotFound)
		return
	}
	rowsAffected, err := q.DeleteRecurring(r.Context(), db.DeleteRecurringParams{ID: id, UserID: userID})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if rowsAffected == 0 {
		httpx.WriteError(w, r, httpx.ErrNotFound)
		return
	}
	httpx.WriteJSON(w, 200, map[string]any{"message": "deleted"})
}

// RunDue materializes all due recurring expenses into real expenses.
// Called on startup and periodically by a ticker in main.
func (h *Handler) RunDue(ctx context.Context) {
	for {
		tx, err := h.Pool.Begin(ctx)
		if err != nil {
			log.Printf("recurring: begin transaction failed: %v", err)
			return
		}
		q := h.ensureQueries()
		if q == nil {
			_ = tx.Rollback(ctx)
			log.Printf("recurring: no queries available")
			return
		}
		qtx := q.WithTx(tx)
		row, err := qtx.ClaimDueRecurring(ctx)
		if err != nil {
			_ = tx.Rollback(ctx)
			if err != pgx.ErrNoRows {
				log.Printf("recurring: claim failed: %v", err)
			}
			return // no due rows
		}

		// Build splits: reuse stored split definitions, or EQUAL across all members
		var splits []map[string]any
		_ = json.Unmarshal(row.Splits, &splits)
		if len(splits) == 0 {
			memberIDs, err := qtx.ListRecurringGroupMembers(ctx, row.GroupID)
			if err != nil {
				log.Printf("recurring: members query failed: %v", err)
				_ = tx.Rollback(ctx)
				continue
			}
			var ids []map[string]any
			for _, mid := range memberIDs {
				ids = append(ids, map[string]any{"user_id": mid.String()})
			}
			splits = ids
		}
		splitsPayload, _ := json.Marshal(splits)
		expenseID := uuid.New()
		expenseDate := pgtype.Date{Time: time.Now(), Valid: true}
		err = qtx.CreateRecurringExpenseInstance(ctx, db.CreateRecurringExpenseInstanceParams{
			ID:          expenseID,
			GroupID:     row.GroupID,
			Description: row.Description + " (recurring)",
			Amount:      row.Amount,
			Currency:    row.Currency,
			PaidBy:      row.PaidBy,
			SplitMode:   row.SplitMode,
			CategoryID:  row.CategoryID,
			Column9:     expenseDate,
		})
		if err != nil {
			log.Printf("recurring: expense insert failed: %v", err)
			_ = tx.Rollback(ctx)
			continue
		}
		// Insert splits with the same distribution logic as the API path
		if err := h.insertSplits(ctx, qtx, expenseID, row.GroupID, row.Amount, row.SplitMode, splitsPayload); err != nil {
			log.Printf("recurring: splits insert failed: %v", err)
			_ = tx.Rollback(ctx)
			continue
		}
		if err = qtx.CreateRecurringActivity(ctx, db.CreateRecurringActivityParams{
			GroupID:  row.GroupID,
			ActorID:  row.PaidBy,
			EntityID: expenseID,
			Column4:  row.Description,
		}); err != nil {
			log.Printf("recurring: activity insert failed: %v", err)
			_ = tx.Rollback(ctx)
			continue
		}
		if err = tx.Commit(ctx); err != nil {
			log.Printf("recurring: commit failed: %v", err)
			continue
		}
	}
}

func (h *Handler) insertSplits(ctx context.Context, q *db.Queries, expenseID uuid.UUID, groupID uuid.UUID, amount int64, splitMode string, splitsPayload []byte) error {
	var splits []map[string]any
	if err := json.Unmarshal(splitsPayload, &splits); err != nil {
		return err
	}
	// Resolve member list for EQUAL
	var ids []uuid.UUID
	for _, s := range splits {
		if v, ok := s["user_id"].(string); ok {
			if uid, err := uuid.Parse(v); err == nil {
				ids = append(ids, uid)
			}
		}
	}
	if len(ids) == 0 {
		return nil
	}
	if splitMode == "EQUAL" || splitMode == "" {
		base := amount / int64(len(ids))
		rem := amount % int64(len(ids))
		for i, uid := range ids {
			amt := base
			if i < int(rem) {
				amt++
			}
			if err := q.CreateRecurringSplit(ctx, db.CreateRecurringSplitParams{ExpenseID: expenseID, UserID: uid, Amount: amt}); err != nil {
				return err
			}
		}
		return nil
	}
	// EXACT/PERCENTAGE/SHARES: use provided values
	for _, s := range splits {
		rawUID, ok := s["user_id"].(string)
		if !ok {
			continue
		}
		uid, err := uuid.Parse(rawUID)
		if err != nil {
			continue
		}
		switch splitMode {
		case "EXACT":
			if v, ok := s["amount"].(float64); ok {
				if err := q.CreateRecurringSplit(ctx, db.CreateRecurringSplitParams{ExpenseID: expenseID, UserID: uid, Amount: int64(v)}); err != nil {
					return err
				}
			}
		case "PERCENTAGE":
			if v, ok := s["percentage"].(float64); ok {
				amt := amount * int64(v) / 100
				var n pgtype.Numeric
				_ = n.Scan(fmt.Sprintf("%v", v))
				if err := q.CreateRecurringSplitWithPercentage(ctx, db.CreateRecurringSplitWithPercentageParams{ExpenseID: expenseID, UserID: uid, Amount: amt, Percentage: n}); err != nil {
					return err
				}
			}
		case "SHARES":
			if v, ok := s["shares"].(float64); ok {
				if err := q.CreateRecurringSplitWithShares(ctx, db.CreateRecurringSplitWithSharesParams{ExpenseID: expenseID, UserID: uid, Shares: pgtype.Int4{Int32: int32(v), Valid: true}}); err != nil {
					return err
				}
			}
		}
	}
	// Ensure sum(splits) == amount for non-equal modes by topping up the payer's share
	sum, err := q.GetRecurringSplitsSum(ctx, expenseID)
	if err != nil {
		return err
	}
	if diff := amount - sum; diff != 0 {
		if err := q.TopUpRecurringSplit(ctx, db.TopUpRecurringSplitParams{ExpenseID: expenseID, UserID: ids[0], Amount: diff}); err != nil {
			return err
		}
	}
	return nil
}
