package recurring

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nabinkhanal00/settlr-api/internal/httpx"
)

type Handler struct {
	Pool *pgxpool.Pool
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
	var ok bool
	_ = h.Pool.QueryRow(r.Context(), `SELECT true FROM group_members WHERE group_id=$1 AND user_id=$2`, groupID, userID).Scan(&ok)
	if !ok {
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
		_ = h.Pool.QueryRow(r.Context(), `SELECT currency FROM groups WHERE id=$1`, groupID).Scan(&currency)
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
	_, err = h.Pool.Exec(r.Context(), `
		INSERT INTO recurring_expenses (id, group_id, created_by, description, amount, currency, category_id, split_mode, splits, paid_by, frequency, next_run_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`,
		id, groupID, userID, req.Description, req.Amount, currency, req.CategoryID, req.SplitMode, string(splitsJSON), req.PaidBy, req.Frequency, start)
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
	var ok bool
	_ = h.Pool.QueryRow(r.Context(), `SELECT true FROM group_members WHERE group_id=$1 AND user_id=$2`, groupID, userID).Scan(&ok)
	if !ok {
		httpx.WriteError(w, r, httpx.ErrGroupMemberRequired)
		return
	}
	rows, err := h.Pool.Query(r.Context(), `
		SELECT id, description, amount, currency, split_mode, paid_by, frequency, next_run_at, last_run_at, active
		FROM recurring_expenses WHERE group_id=$1 ORDER BY created_at DESC`, groupID)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	defer rows.Close()
	var out []map[string]any
	for rows.Next() {
		var id, paidBy uuid.UUID
		var description, splitMode, frequency, currency string
		var amount int64
		var nextRun, lastRun *time.Time
		var active bool
		if err := rows.Scan(&id, &description, &amount, &currency, &splitMode, &paidBy, &frequency, &nextRun, &lastRun, &active); err != nil {
			httpx.WriteError(w, r, err)
			return
		}
		out = append(out, map[string]any{
			"id": id, "description": description, "amount": amount, "currency": currency,
			"split_mode": splitMode, "paid_by": paidBy, "frequency": frequency,
			"next_run_at": nextRun, "last_run_at": lastRun, "active": active,
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
	// Only group members of the recurring expense's group may update it
	var ok bool
	_ = h.Pool.QueryRow(r.Context(), `
		SELECT true FROM recurring_expenses re
		JOIN group_members gm ON gm.group_id=re.group_id AND gm.user_id=$2
		WHERE re.id=$1`, id, userID).Scan(&ok)
	if !ok {
		httpx.WriteError(w, r, httpx.ErrGroupMemberRequired)
		return
	}
	res, err := h.Pool.Exec(r.Context(), `UPDATE recurring_expenses SET active=$3, updated_at=now() WHERE id=$1 AND group_id IN (SELECT group_id FROM group_members WHERE user_id=$2)`, id, userID, *req.Active)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if res.RowsAffected() == 0 {
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
	res, err := h.Pool.Exec(r.Context(), `
		DELETE FROM recurring_expenses re
		USING group_members gm
		WHERE re.id=$1 AND gm.group_id=re.group_id AND gm.user_id=$2`, id, userID)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if res.RowsAffected() == 0 {
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
		var id, groupID, paidBy uuid.UUID
		var description, currency, splitMode string
		var amount int64
		var categoryID *uuid.UUID
		var splitsJSON []byte
		var nextRun time.Time
		err = tx.QueryRow(ctx, `
			UPDATE recurring_expenses
			SET next_run_at = CASE frequency
			                    WHEN 'DAILY' THEN next_run_at + interval '1 day'
			                    WHEN 'WEEKLY' THEN next_run_at + interval '7 days'
			                    WHEN 'MONTHLY' THEN next_run_at + interval '1 month'
			                    ELSE next_run_at + interval '1 year' END,
			    last_run_at = next_run_at,
			    updated_at = now()
			WHERE id = (
				SELECT id FROM recurring_expenses
				WHERE active = true AND next_run_at <= now()
				ORDER BY next_run_at
				FOR UPDATE SKIP LOCKED
				LIMIT 1
			)
			RETURNING id, group_id, description, amount, currency, split_mode, category_id, splits, paid_by, next_run_at`,
		).Scan(&id, &groupID, &description, &amount, &currency, &splitMode, &categoryID, &splitsJSON, &paidBy, &nextRun)
		if err != nil {
			_ = tx.Rollback(ctx)
			if err != pgx.ErrNoRows {
				log.Printf("recurring: claim failed: %v", err)
			}
			return // no due rows
		}

		// Build splits: reuse stored split definitions, or EQUAL across all members
		var splits []map[string]any
		_ = json.Unmarshal(splitsJSON, &splits)
		if len(splits) == 0 {
			mrows, err := tx.Query(ctx, `SELECT user_id FROM group_members WHERE group_id=$1`, groupID)
			if err != nil {
				log.Printf("recurring: members query failed: %v", err)
				_ = tx.Rollback(ctx)
				continue
			}
			var ids []map[string]any
			for mrows.Next() {
				var mid uuid.UUID
				_ = mrows.Scan(&mid)
				ids = append(ids, map[string]any{"user_id": mid.String()})
			}
			mrows.Close()
			splits = ids
		}
		splitsPayload, _ := json.Marshal(splits)
		expenseID := uuid.New()
		_, err = tx.Exec(ctx, `
			INSERT INTO expenses (id, group_id, description, amount, currency, paid_by, split_mode, category_id, expense_date, created_by)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9::date,$6)`,
			expenseID, groupID, description+" (recurring)", amount, currency, paidBy, splitMode, categoryID, time.Now().Format("2006-01-02"))
		if err != nil {
			log.Printf("recurring: expense insert failed: %v", err)
			_ = tx.Rollback(ctx)
			continue
		}
		// Insert splits with the same distribution logic as the API path
		if err := h.insertSplits(ctx, tx, expenseID, groupID, amount, splitMode, splitsPayload); err != nil {
			log.Printf("recurring: splits insert failed: %v", err)
			_ = tx.Rollback(ctx)
			continue
		}
		if _, err = tx.Exec(ctx, `
			INSERT INTO activity_events (group_id, actor_id, type, entity_type, entity_id, payload)
			VALUES ($1,$2,'EXPENSE_ADDED','expense',$3,json_build_object('description',$4::text,'recurring',true))`,
			groupID, paidBy, expenseID, description); err != nil {
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

type executor interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

func (h *Handler) insertSplits(ctx context.Context, db executor, expenseID uuid.UUID, groupID uuid.UUID, amount int64, splitMode string, splitsPayload []byte) error {
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
			if _, err := db.Exec(ctx, `INSERT INTO expense_splits (id, expense_id, user_id, amount) VALUES (gen_random_uuid(),$1,$2,$3)`, expenseID, uid, amt); err != nil {
				return err
			}
		}
		return nil
	}
	// EXACT/PERCENTAGE/SHARES: use provided values
	for _, s := range splits {
		uid, err := uuid.Parse(s["user_id"].(string))
		if err != nil {
			continue
		}
		switch splitMode {
		case "EXACT":
			if v, ok := s["amount"].(float64); ok {
				if _, err := db.Exec(ctx, `INSERT INTO expense_splits (id, expense_id, user_id, amount) VALUES (gen_random_uuid(),$1,$2,$3)`, expenseID, uid, int64(v)); err != nil {
					return err
				}
			}
		case "PERCENTAGE":
			if v, ok := s["percentage"].(float64); ok {
				if _, err := db.Exec(ctx, `INSERT INTO expense_splits (id, expense_id, user_id, amount, percentage) VALUES (gen_random_uuid(),$1,$2,$3,$4)`, expenseID, uid, amount*int64(v)/100, v); err != nil {
					return err
				}
			}
		case "SHARES":
			if v, ok := s["shares"].(float64); ok {
				if _, err := db.Exec(ctx, `INSERT INTO expense_splits (id, expense_id, user_id, shares) VALUES (gen_random_uuid(),$1,$2,$3)`, expenseID, uid, int64(v)); err != nil {
					return err
				}
			}
		}
	}
	// Ensure sum(splits) == amount for non-equal modes by topping up the payer's share
	var sum int64
	if err := db.QueryRow(ctx, `SELECT coalesce(sum(amount),0) FROM expense_splits WHERE expense_id=$1`, expenseID).Scan(&sum); err != nil {
		return err
	}
	if diff := amount - sum; diff != 0 {
		if _, err := db.Exec(ctx, `UPDATE expense_splits SET amount = amount + $3 WHERE expense_id=$1 AND user_id=$2`, expenseID, ids[0], diff); err != nil {
			return err
		}
	}
	return nil
}
