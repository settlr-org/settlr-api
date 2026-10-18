package users

import (
	"context"
	"net/http"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	db "github.com/settlr-org/settlr-api/internal/db"
	"github.com/settlr-org/settlr-api/internal/httpx"
)

// walletCounts reports how many rows of each kind a deletion removed.
type walletCounts struct {
	Accounts               int64 `json:"accounts"`
	Transactions           int64 `json:"transactions"`
	PlannedPayments        int64 `json:"planned_payments"`
	Loans                  int64 `json:"loans"`
	Budgets                int64 `json:"budgets"`
	Tags                   int64 `json:"tags"`
	Categories             int64 `json:"categories"`
	RateOverrides          int64 `json:"rate_overrides"`
	LegacyPersonalExpenses int64 `json:"legacy_personal_expenses"`
}

// wipeWalletData removes everything the user owns in the wallet: accounts and their transactions, tags, planned
// payments, budgets, loans (with records), exchange-rate overrides, their own categories, display settings and the
// legacy personal-expense tables. It must run inside the caller's transaction. Group data, friendships and the
// profile are left alone. Links from group expenses and settlements to the deleted accounts are cleared by their
// ON DELETE SET NULL foreign keys.
func wipeWalletData(ctx context.Context, q *db.Queries, userID uuid.UUID) (walletCounts, error) {
	var c walletCounts
	var err error
	if c.Transactions, err = q.DeleteUserTransactions(ctx, userID); err != nil {
		return c, err
	}
	if c.PlannedPayments, err = q.DeleteUserPlannedPayments(ctx, userID); err != nil {
		return c, err
	}
	if c.Loans, err = q.DeleteUserLoans(ctx, userID); err != nil {
		return c, err
	}
	if c.Budgets, err = q.DeleteUserBudgets(ctx, userID); err != nil {
		return c, err
	}
	if c.Tags, err = q.DeleteUserTags(ctx, userID); err != nil {
		return c, err
	}
	if c.Accounts, err = q.DeleteUserAccounts(ctx, userID); err != nil {
		return c, err
	}
	if c.RateOverrides, err = q.DeleteUserRateOverrides(ctx, userID); err != nil {
		return c, err
	}
	if err = q.DeleteUserCategoryOrder(ctx, userID); err != nil {
		return c, err
	}
	if err = q.DeleteUserSettings(ctx, userID); err != nil {
		return c, err
	}
	if c.LegacyPersonalExpenses, err = q.DeleteUserLegacyPersonalExpenses(ctx, userID); err != nil {
		return c, err
	}
	if err = q.DeleteUserLegacyPersonalBudgets(ctx, userID); err != nil {
		return c, err
	}
	if c.Categories, err = q.DeleteUserOwnedCategories(ctx, userID); err != nil {
		return c, err
	}
	return c, nil
}

// DeleteWalletData handles DELETE /api/v1/me/wallet-data?confirm=DELETE: it erases all wallet data but keeps the
// account, profile, groups and friends ("delete all my data" in Ivy Wallet terms).
func (h *Handler) DeleteWalletData(w http.ResponseWriter, r *http.Request) {
	uid, _ := httpx.GetUserID(r.Context())
	id, err := uuid.Parse(uid)
	if err != nil {
		httpx.WriteError(w, r, httpx.ErrUnauthorized)
		return
	}
	if r.URL.Query().Get("confirm") != "DELETE" {
		httpx.WriteError(w, r, &httpx.AppError{Code: "VALIDATION_ERROR", Message: "confirm=DELETE is required to erase wallet data", Status: 422})
		return
	}
	if h.Pool == nil {
		httpx.WriteError(w, r, httpx.ErrNotFound)
		return
	}
	tx, err := h.Pool.Begin(r.Context())
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	defer tx.Rollback(r.Context()) //nolint:errcheck // no-op after commit
	counts, err := wipeWalletData(r.Context(), db.New(h.Pool).WithTx(tx), id)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, 200, map[string]any{"message": "wallet data deleted", "deleted": counts})
}

// DeleteAccount handles DELETE /api/v1/me. It erases the user's wallet data and personal records (sessions, Google
// link, tokens, notifications, pending invitations to their e-mail address, payment details) and anonymises the
// user row. The row itself is kept only because shared group history (expenses, splits, settlements, comments)
// still references it; it no longer identifies anyone and cannot sign in again, with a password or with Google.
func (h *Handler) DeleteAccount(w http.ResponseWriter, r *http.Request) {
	uid, _ := httpx.GetUserID(r.Context())
	id, err := uuid.Parse(uid)
	if err != nil {
		httpx.WriteError(w, r, httpx.ErrUnauthorized)
		return
	}
	if h.Pool == nil {
		httpx.WriteError(w, r, httpx.ErrNotFound)
		return
	}
	ctx := r.Context()
	me, err := db.New(h.Pool).GetMe(ctx, id)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	tx, err := h.Pool.Begin(ctx)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after commit
	q := db.New(h.Pool).WithTx(tx)

	if _, err = wipeWalletData(ctx, q, id); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	steps := []func() error{
		func() error { return q.DeletePendingGroupInvitesForEmail(ctx, me.Email) },
		func() error { return q.DeleteUserNotifications(ctx, id) },
		func() error { return q.DeleteUserNotificationPreferences(ctx, id) },
		func() error { return q.DeleteUserOAuthIdentities(ctx, id) },
		func() error { return q.DeleteUserEmailVerificationTokens(ctx, id) },
		func() error { return q.DeleteUserPasswordResetTokens(ctx, id) },
		func() error { return q.DeleteUserFriendInvites(ctx, id) },
		func() error { return q.DeleteUserSessions(ctx, id) },
		func() error {
			return q.SoftDeleteUser(ctx, db.SoftDeleteUserParams{Email: "deleted_" + id.String() + "@deleted.local", ID: id})
		},
	}
	for _, step := range steps {
		if err = step(); err != nil {
			httpx.WriteError(w, r, err)
			return
		}
	}
	if err = tx.Commit(ctx); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if h.AuthSvc != nil {
		h.AuthSvc.DenyUser(id)
	}
	httpx.WriteJSON(w, 200, map[string]any{"message": "account deleted"})
}

// PurgeStaleGuests deletes guest accounts that can never be signed in to again (every session expired or revoked,
// older than 30 days) together with their wallet data. Guests cannot touch group features, so nothing shared
// references them. It returns how many accounts were removed.
func PurgeStaleGuests(ctx context.Context, pool *pgxpool.Pool) (int, error) {
	q := db.New(pool)
	ids, err := q.ListStaleGuests(ctx)
	if err != nil {
		return 0, err
	}
	removed := 0
	for _, id := range ids {
		tx, err := pool.Begin(ctx)
		if err != nil {
			return removed, err
		}
		tq := q.WithTx(tx)
		if _, err = wipeWalletData(ctx, tq, id); err == nil {
			err = tq.DeleteUserNotifications(ctx, id)
		}
		if err == nil {
			err = tq.DeleteUserRow(ctx, id)
		}
		if err == nil {
			err = tx.Commit(ctx)
		}
		_ = tx.Rollback(ctx) // no-op after commit
		if err != nil {
			return removed, err
		}
		removed++
	}
	return removed, nil
}
