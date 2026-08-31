package users

import (
	"net/http"
	"regexp"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/settlr-org/settlr-api/internal/auth"
	"github.com/settlr-org/settlr-api/internal/httpx"
)

var emailRe = regexp.MustCompile(`^[^@\s]+@[^@\s]+\.[^@\s]+$`)

type Handler struct {
	Pool    *pgxpool.Pool
	AuthSvc *auth.Service
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
	var p paymentInfo
	if err := h.Pool.QueryRow(r.Context(), `SELECT coalesce(bank_qr_url,''), coalesce(bank_name,''), coalesce(payment_handle,'') FROM users WHERE id=$1`, id).Scan(&p.BankQRURL, &p.BankName, &p.PaymentHandle); err != nil {
		httpx.WriteError(w, r, httpx.ErrNotFound)
		return
	}
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
	_, err := h.Pool.Exec(r.Context(), `UPDATE users SET bank_qr_url=$1, bank_name=$2, payment_handle=$3, updated_at=now() WHERE id=$4`, strings.TrimSpace(p.BankQRURL), strings.TrimSpace(p.BankName), strings.TrimSpace(p.PaymentHandle), id)
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
	var accepted bool
	_ = h.Pool.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM friendships WHERE ((user_id=$1 AND friend_id=$2) OR (user_id=$2 AND friend_id=$1)) AND status='ACCEPTED')`, viewer, id).Scan(&accepted)
	if !accepted {
		httpx.WriteError(w, r, httpx.ErrForbidden)
		return
	}
	var p paymentInfo
	if err := h.Pool.QueryRow(r.Context(), `SELECT coalesce(bank_qr_url,''), coalesce(bank_name,''), coalesce(payment_handle,'') FROM users WHERE id=$1 AND is_anonymous=false`, id).Scan(&p.BankQRURL, &p.BankName, &p.PaymentHandle); err != nil {
		httpx.WriteError(w, r, httpx.ErrNotFound)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, p)
}

func (h *Handler) GetMe(w http.ResponseWriter, r *http.Request) {
	uid, _ := httpx.GetUserID(r.Context())
	id, _ := uuid.Parse(uid)
	var name, email, avatar, currency, tz string
	var verified *string
	var hasPassword bool
	err := h.Pool.QueryRow(r.Context(),
		`SELECT name, email, coalesce(avatar_url,''), default_currency, timezone, email_verified_at::text, password_hash IS NOT NULL FROM users WHERE id=$1`, id).
		Scan(&name, &email, &avatar, &currency, &tz, &verified, &hasPassword)
	if err != nil {
		httpx.WriteError(w, r, httpx.ErrNotFound)
		return
	}
	httpx.WriteJSON(w, 200, map[string]any{"id": id, "name": name, "email": email, "avatar_url": avatar, "default_currency": currency, "timezone": tz, "email_verified": verified != nil, "has_password": hasPassword})
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
	if req.Name != nil {
		n := strings.TrimSpace(*req.Name)
		if n == "" || len(n) > 100 {
			httpx.WriteJSON(w, 422, map[string]any{"error": map[string]string{"code": "VALIDATION_ERROR", "message": "invalid name"}})
			return
		}
		_, _ = h.Pool.Exec(r.Context(), `UPDATE users SET name=$1, updated_at=now() WHERE id=$2`, n, id)
	}
	if req.Email != nil {
		e := strings.TrimSpace(strings.ToLower(*req.Email))
		if !emailRe.MatchString(e) {
			httpx.WriteJSON(w, 422, map[string]any{"error": map[string]string{"code": "VALIDATION_ERROR", "message": "invalid email"}})
			return
		}
		_, err := h.Pool.Exec(r.Context(), `UPDATE users SET email=$1, updated_at=now(), email_verified_at=NULL WHERE id=$2`, e, id)
		if err != nil && strings.Contains(err.Error(), "duplicate") {
			httpx.WriteJSON(w, 409, map[string]any{"error": map[string]string{"code": "CONFLICT", "message": "email already in use"}})
			return
		}
	}
	if req.AvatarURL != nil {
		_, _ = h.Pool.Exec(r.Context(), `UPDATE users SET avatar_url=$1, updated_at=now() WHERE id=$2`, *req.AvatarURL, id)
	}
	if req.DefaultCurrency != nil {
		c := strings.ToUpper(strings.TrimSpace(*req.DefaultCurrency))
		if !auth.SupportedCurrencies[c] {
			httpx.WriteJSON(w, 422, map[string]any{"error": map[string]string{"code": "VALIDATION_ERROR", "message": "unsupported currency"}})
			return
		}
		_, _ = h.Pool.Exec(r.Context(), `UPDATE users SET default_currency=$1, updated_at=now() WHERE id=$2`, c, id)
	}
	if req.Timezone != nil {
		_, _ = h.Pool.Exec(r.Context(), `UPDATE users SET timezone=$1, updated_at=now() WHERE id=$2`, *req.Timezone, id)
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
	var hash *string
	if err := h.Pool.QueryRow(r.Context(), `SELECT password_hash FROM users WHERE id=$1`, id).Scan(&hash); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if hash != nil && !auth.VerifyPassword(*hash, req.CurrentPassword) {
		httpx.WriteJSON(w, 401, map[string]any{"error": map[string]string{"code": "UNAUTHORIZED", "message": "current password incorrect"}})
		return
	}
	newHash, _ := auth.HashPassword(req.NewPassword)
	_, _ = h.Pool.Exec(r.Context(), `UPDATE users SET password_hash=$1, updated_at=now() WHERE id=$2`, newHash, id)
	_ = h.AuthSvc.RevokeAllSessions(r.Context(), id)
	httpx.WriteJSON(w, 200, map[string]any{"message": "password updated"})
}

func (h *Handler) DeleteAccount(w http.ResponseWriter, r *http.Request) {
	uid, _ := httpx.GetUserID(r.Context())
	id, _ := uuid.Parse(uid)
	// Anonymize user instead of hard delete to preserve financial records
	anonEmail := "deleted_" + id.String() + "@deleted.local"
	_, err := h.Pool.Exec(r.Context(),
		`UPDATE users SET name='Deleted User', email=$1, password_hash='deleted', avatar_url=NULL, is_anonymous=true, updated_at=now() WHERE id=$2`,
		anonEmail, id)
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
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if len(q) < 1 {
		httpx.WriteJSON(w, 200, map[string]any{"data": []any{}})
		return
	}
	rows, err := h.Pool.Query(r.Context(),
		`SELECT u.id, u.name, u.email, coalesce(u.avatar_url,''),
			EXISTS (SELECT 1 FROM friendships f
				WHERE f.user_id=LEAST($2::uuid, u.id) AND f.friend_id=GREATEST($2::uuid, u.id)
				AND f.status='PENDING' AND f.action_by=$2)
		 FROM users u
		 WHERE u.id <> $2 AND u.is_anonymous=false
		   AND (u.name ILIKE '%' || $1 || '%' OR u.email ILIKE '%' || $1 || '%')
		 ORDER BY u.name LIMIT 20`, q, currentID)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	defer rows.Close()
	type u struct {
		ID        uuid.UUID `json:"id"`
		Name      string    `json:"name"`
		Email     string    `json:"email"`
		AvatarURL string    `json:"avatar_url"`
		Requested bool      `json:"requested"`
	}
	var out []u
	for rows.Next() {
		var x u
		if err := rows.Scan(&x.ID, &x.Name, &x.Email, &x.AvatarURL, &x.Requested); err != nil {
			httpx.WriteError(w, r, err)
			return
		}
		out = append(out, x)
	}
	if out == nil {
		out = []u{}
	}
	if err := rows.Err(); err != nil {
		httpx.WriteError(w, r, err)
		return
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
	var name, avatar string
	err = h.Pool.QueryRow(r.Context(), `SELECT name, coalesce(avatar_url,'') FROM users WHERE id=$1 AND is_anonymous=false`, id).Scan(&name, &avatar)
	if err == pgx.ErrNoRows {
		httpx.WriteError(w, r, httpx.ErrNotFound)
		return
	}
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, 200, map[string]any{"id": id, "name": name, "avatar_url": avatar})
}
