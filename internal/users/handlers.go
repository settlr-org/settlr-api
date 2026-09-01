package users

import (
	"context"
	"net/http"
	"regexp"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/settlr-org/settlr-api/internal/auth"
	db "github.com/settlr-org/settlr-api/internal/db"
	"github.com/settlr-org/settlr-api/internal/httpx"
)

var emailRe = regexp.MustCompile(`^[^@\s]+@[^@\s]+\.[^@\s]+$`)

type Handler struct {
	Pool    *pgxpool.Pool
	Queries *db.Queries
	AuthSvc *auth.Service
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
	mux.Handle("GET /api/v1/me", authMw(http.HandlerFunc(h.GetMe)))
	mux.Handle("PATCH /api/v1/me", authMw(http.HandlerFunc(h.UpdateMe)))
	mux.Handle("PATCH /api/v1/me/password", authMw(http.HandlerFunc(h.ChangePassword)))
	mux.Handle("DELETE /api/v1/me", authMw(http.HandlerFunc(h.DeleteAccount)))
	mux.Handle("GET /api/v1/users/search", authMw(http.HandlerFunc(h.SearchUsers)))
	mux.Handle("GET /api/v1/users/{id}", authMw(http.HandlerFunc(h.GetUser)))
	mux.Handle("GET /api/v1/me/payment-info", authMw(http.HandlerFunc(h.GetPaymentInfo)))
	mux.Handle("PUT /api/v1/me/payment-info", authMw(http.HandlerFunc(h.UpdatePaymentInfo)))
	mux.Handle("GET /api/v1/users/{id}/payment-info", authMw(http.HandlerFunc(h.GetFriendPaymentInfo)))
}

type paymentInfo struct {
	BankQRURL     string `json:"bank_qr_url"`
	BankName      string `json:"bank_name"`
	PaymentHandle string `json:"payment_handle"`
}

func (h *Handler) GetPaymentInfo(w http.ResponseWriter, r *http.Request) {
	uid, _ := httpx.GetUserID(r.Context())
	id, _ := uuid.Parse(uid)
	q := h.ensureQueries()
	if q == nil {
		httpx.WriteError(w, r, httpx.ErrNotFound)
		return
	}
	row, err := q.GetUserPaymentInfo(r.Context(), id)
	if err != nil {
		httpx.WriteError(w, r, httpx.ErrNotFound)
		return
	}
	p := paymentInfo{BankQRURL: row.BankQrUrl, BankName: row.BankName, PaymentHandle: row.PaymentHandle}
	httpx.WriteJSON(w, http.StatusOK, p)
}

func (h *Handler) UpdatePaymentInfo(w http.ResponseWriter, r *http.Request) {
	uid, _ := httpx.GetUserID(r.Context())
	id, _ := uuid.Parse(uid)
	var p paymentInfo
	if err := httpx.DecodeJSON(r, &p); err != nil {
		httpx.WriteJSON(w, http.StatusBadRequest, map[string]any{"error": map[string]string{"code": "VALIDATION_ERROR", "message": err.Error()}})
		return
	}
	if len(p.BankQRURL) > 2048 || len(p.BankName) > 120 || len(p.PaymentHandle) > 160 {
		httpx.WriteJSON(w, http.StatusUnprocessableEntity, map[string]any{"error": map[string]string{"code": "VALIDATION_ERROR", "message": "payment information is too long"}})
		return
	}
	q := h.ensureQueries()
	if q == nil {
		httpx.WriteError(w, r, httpx.ErrNotFound)
		return
	}
	p.BankQRURL = strings.TrimSpace(p.BankQRURL)
	p.BankName = strings.TrimSpace(p.BankName)
	p.PaymentHandle = strings.TrimSpace(p.PaymentHandle)
	err := q.UpdateUserPaymentInfo(r.Context(), db.UpdateUserPaymentInfoParams{
		BankQrUrl:     pgtype.Text{String: p.BankQRURL, Valid: true},
		BankName:      pgtype.Text{String: p.BankName, Valid: true},
		PaymentHandle: pgtype.Text{String: p.PaymentHandle, Valid: true},
		ID:            id,
	})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, p)
}

func (h *Handler) GetFriendPaymentInfo(w http.ResponseWriter, r *http.Request) {
	uid, _ := httpx.GetUserID(r.Context())
	viewer, _ := uuid.Parse(uid)
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		httpx.WriteError(w, r, &httpx.AppError{Code: "VALIDATION_ERROR", Message: "invalid user id", Status: 422})
		return
	}
	q := h.ensureQueries()
	if q == nil {
		httpx.WriteError(w, r, httpx.ErrForbidden)
		return
	}
	accepted, err := q.CheckFriendshipAccepted(r.Context(), db.CheckFriendshipAcceptedParams{UserID: viewer, FriendID: id})
	if err != nil || !accepted {
		httpx.WriteError(w, r, httpx.ErrForbidden)
		return
	}
	row, err := q.GetUserPaymentInfoForFriend(r.Context(), id)
	if err != nil {
		httpx.WriteError(w, r, httpx.ErrNotFound)
		return
	}
	p := paymentInfo{BankQRURL: row.BankQrUrl, BankName: row.BankName, PaymentHandle: row.PaymentHandle}
	httpx.WriteJSON(w, http.StatusOK, p)
}

func (h *Handler) GetMe(w http.ResponseWriter, r *http.Request) {
	uid, _ := httpx.GetUserID(r.Context())
	id, _ := uuid.Parse(uid)
	q := h.ensureQueries()
	if q == nil {
		httpx.WriteError(w, r, httpx.ErrNotFound)
		return
	}
	row, err := q.GetMe(r.Context(), id)
	if err != nil {
		httpx.WriteError(w, r, httpx.ErrNotFound)
		return
	}
	hasPassword := false
	if b, ok := row.HasPassword.(bool); ok {
		hasPassword = b
	} else if s, ok := row.HasPassword.(string); ok {
		hasPassword = s == "t" || s == "true"
	}
	emailVerified := row.EmailVerifiedAt.Valid
	httpx.WriteJSON(w, 200, map[string]any{"id": id, "name": row.Name, "email": row.Email, "avatar_url": row.AvatarUrl, "default_currency": row.DefaultCurrency, "timezone": row.Timezone, "email_verified": emailVerified, "has_password": hasPassword})
}

type updateMeReq struct {
	Name            *string `json:"name"`
	Email           *string `json:"email"`
	AvatarURL       *string `json:"avatar_url"`
	DefaultCurrency *string `json:"default_currency"`
	Timezone        *string `json:"timezone"`
}

func (h *Handler) UpdateMe(w http.ResponseWriter, r *http.Request) {
	uid, _ := httpx.GetUserID(r.Context())
	id, _ := uuid.Parse(uid)
	var req updateMeReq
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteJSON(w, 400, map[string]any{"error": map[string]string{"code": "VALIDATION_ERROR", "message": err.Error()}})
		return
	}
	q := h.ensureQueries()
	if q == nil {
		httpx.WriteError(w, r, httpx.ErrNotFound)
		return
	}
	if req.Name != nil {
		n := strings.TrimSpace(*req.Name)
		if n == "" || len(n) > 100 {
			httpx.WriteJSON(w, 422, map[string]any{"error": map[string]string{"code": "VALIDATION_ERROR", "message": "invalid name"}})
			return
		}
		_ = q.UpdateUserName(r.Context(), db.UpdateUserNameParams{Name: n, ID: id})
	}
	if req.Email != nil {
		e := strings.TrimSpace(strings.ToLower(*req.Email))
		if !emailRe.MatchString(e) {
			httpx.WriteJSON(w, 422, map[string]any{"error": map[string]string{"code": "VALIDATION_ERROR", "message": "invalid email"}})
			return
		}
		err := q.UpdateUserEmail(r.Context(), db.UpdateUserEmailParams{Email: e, ID: id})
		if err != nil && strings.Contains(err.Error(), "duplicate") {
			httpx.WriteJSON(w, 409, map[string]any{"error": map[string]string{"code": "CONFLICT", "message": "email already in use"}})
			return
		}
		if err != nil {
			// handle other errors if needed
		}
	}
	if req.AvatarURL != nil {
		_ = q.UpdateUserAvatar(r.Context(), db.UpdateUserAvatarParams{AvatarUrl: pgtype.Text{String: *req.AvatarURL, Valid: true}, ID: id})
	}
	if req.DefaultCurrency != nil {
		c := strings.ToUpper(strings.TrimSpace(*req.DefaultCurrency))
		if !auth.SupportedCurrencies[c] {
			httpx.WriteJSON(w, 422, map[string]any{"error": map[string]string{"code": "VALIDATION_ERROR", "message": "unsupported currency"}})
			return
		}
		_ = q.UpdateUserDefaultCurrency(r.Context(), db.UpdateUserDefaultCurrencyParams{DefaultCurrency: c, ID: id})
	}
	if req.Timezone != nil {
		_ = q.UpdateUserTimezone(r.Context(), db.UpdateUserTimezoneParams{Timezone: *req.Timezone, ID: id})
	}
	h.GetMe(w, r)
}

type changePwReq struct {
	CurrentPassword string `json:"current_password"`
	NewPassword     string `json:"new_password"`
}

func (h *Handler) ChangePassword(w http.ResponseWriter, r *http.Request) {
	uid, _ := httpx.GetUserID(r.Context())
	id, _ := uuid.Parse(uid)
	var req changePwReq
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteJSON(w, 400, map[string]any{"error": map[string]string{"code": "VALIDATION_ERROR", "message": err.Error()}})
		return
	}
	if len(req.NewPassword) < 8 {
		httpx.WriteJSON(w, 422, map[string]any{"error": map[string]string{"code": "VALIDATION_ERROR", "message": "new password must be >= 8 chars"}})
		return
	}
	q := h.ensureQueries()
	if q == nil {
		httpx.WriteError(w, r, httpx.ErrNotFound)
		return
	}
	hash, err := q.GetUserPasswordHash(r.Context(), id)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if hash.Valid && !auth.VerifyPassword(hash.String, req.CurrentPassword) {
		httpx.WriteJSON(w, 401, map[string]any{"error": map[string]string{"code": "UNAUTHORIZED", "message": "current password incorrect"}})
		return
	}
	newHash, _ := auth.HashPassword(req.NewPassword)
	_ = q.UpdateUserPasswordHash(r.Context(), db.UpdateUserPasswordHashParams{PasswordHash: pgtype.Text{String: newHash, Valid: true}, ID: id})
	_ = h.AuthSvc.RevokeAllSessions(r.Context(), id)
	httpx.WriteJSON(w, 200, map[string]any{"message": "password updated"})
}

func (h *Handler) DeleteAccount(w http.ResponseWriter, r *http.Request) {
	uid, _ := httpx.GetUserID(r.Context())
	id, _ := uuid.Parse(uid)
	q := h.ensureQueries()
	if q == nil {
		httpx.WriteError(w, r, httpx.ErrNotFound)
		return
	}
	anonEmail := "deleted_" + id.String() + "@deleted.local"
	err := q.SoftDeleteUser(r.Context(), db.SoftDeleteUserParams{Email: anonEmail, ID: id})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	_ = h.AuthSvc.RevokeAllSessions(r.Context(), id)
	httpx.WriteJSON(w, 200, map[string]any{"message": "account deleted"})
}

func (h *Handler) SearchUsers(w http.ResponseWriter, r *http.Request) {
	uid, _ := httpx.GetUserID(r.Context())
	currentID, err := uuid.Parse(uid)
	if err != nil {
		httpx.WriteError(w, r, httpx.ErrUnauthorized)
		return
	}
	qStr := strings.TrimSpace(r.URL.Query().Get("q"))
	if len(qStr) < 1 {
		httpx.WriteJSON(w, 200, map[string]any{"data": []any{}})
		return
	}
	q := h.ensureQueries()
	if q == nil {
		httpx.WriteError(w, r, httpx.ErrNotFound)
		return
	}
	rows, err := q.SearchUsersByName(r.Context(), db.SearchUsersByNameParams{
		Column1: pgtype.Text{String: qStr, Valid: true},
		Column2: pgtype.UUID{Bytes: currentID, Valid: true},
	})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	type u struct {
		ID        uuid.UUID `json:"id"`
		Name      string    `json:"name"`
		Email     string    `json:"email"`
		AvatarURL string    `json:"avatar_url"`
		Requested bool      `json:"requested"`
	}
	var out []u
	for _, row := range rows {
		out = append(out, u{ID: row.ID, Name: row.Name, Email: row.Email, AvatarURL: row.AvatarUrl, Requested: row.Requested})
	}
	if out == nil {
		out = []u{}
	}
	httpx.WriteJSON(w, 200, map[string]any{"data": out})
}

func (h *Handler) GetUser(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		httpx.WriteError(w, r, &httpx.AppError{Code: "VALIDATION_ERROR", Message: "invalid user id", Status: 422})
		return
	}
	q := h.ensureQueries()
	if q == nil {
		httpx.WriteError(w, r, httpx.ErrNotFound)
		return
	}
	row, err := q.GetUserPublic(r.Context(), id)
	if err == pgx.ErrNoRows {
		httpx.WriteError(w, r, httpx.ErrNotFound)
		return
	}
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, 200, map[string]any{"id": id, "name": row.Name, "avatar_url": row.AvatarUrl})
}
