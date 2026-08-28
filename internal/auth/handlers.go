package auth

import (
	"encoding/json"
	"net/http"
	"regexp"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nabinkhanal00/settlr-api/internal/httpx"
	"github.com/nabinkhanal00/settlr-api/internal/mailer"
)

var emailRe = regexp.MustCompile(`^[^@\s]+@[^@\s]+\.[^@\s]+$`)

type Handler struct {
	Svc    *Service
	Mailer *mailer.Mailer
}

func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/auth/register", h.Register)
	mux.HandleFunc("POST /api/v1/auth/login", h.Login)
	mux.HandleFunc("POST /api/v1/auth/refresh", h.Refresh)
	mux.HandleFunc("POST /api/v1/auth/logout", h.Logout)
	mux.HandleFunc("POST /api/v1/auth/forgot-password", h.ForgotPassword)
	mux.HandleFunc("POST /api/v1/auth/reset-password", h.ResetPassword)
	mux.HandleFunc("POST /api/v1/auth/verify-email", h.VerifyEmail)
	mux.HandleFunc("POST /api/v1/auth/resend-verification", h.ResendVerification)
	mux.HandleFunc("GET /api/v1/auth/sessions", h.ListSessions)
	mux.HandleFunc("DELETE /api/v1/auth/sessions/{id}", h.RevokeSessionByID)
	mux.HandleFunc("DELETE /api/v1/auth/sessions", h.RevokeAllSessions)
}

type registerReq struct {
	Name     string `json:"name"`
	Email    string `json:"email"`
	Password string `json:"password"`
}

func (h *Handler) Register(w http.ResponseWriter, r *http.Request) {
	var req registerReq
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteJSON(w, 400, map[string]any{"error": map[string]string{"code": "VALIDATION_ERROR", "message": err.Error()}})
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	req.Email = strings.TrimSpace(strings.ToLower(req.Email))
	if req.Name == "" || len(req.Name) > 100 {
		httpx.WriteJSON(w, 422, map[string]any{"error": map[string]string{"code": "VALIDATION_ERROR", "message": "name is required (max 100 chars)"}})
		return
	}
	if !emailRe.MatchString(req.Email) {
		httpx.WriteJSON(w, 422, map[string]any{"error": map[string]string{"code": "VALIDATION_ERROR", "message": "invalid email"}})
		return
	}
	if len(req.Password) < 8 || len(req.Password) > 128 {
		httpx.WriteJSON(w, 422, map[string]any{"error": map[string]string{"code": "VALIDATION_ERROR", "message": "password must be 8-128 characters"}})
		return
	}
	hash, err := HashPassword(req.Password)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	id := uuid.New()
	_, err = h.Svc.Pool.Exec(r.Context(),
		`INSERT INTO users (id, name, email, password_hash) VALUES ($1,$2,$3,$4)`,
		id, req.Name, req.Email, hash)
	if err != nil && strings.Contains(err.Error(), "duplicate") {
		httpx.WriteJSON(w, 409, map[string]any{"error": map[string]string{"code": "CONFLICT", "message": "email already registered"}})
		return
	}
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	// Accounts remain inactive until the owner proves they control the address.
	verificationToken, verificationHash := GenerateRefreshToken()
	_, _ = h.Svc.Pool.Exec(r.Context(),
		`INSERT INTO email_verification_tokens (user_id, token_hash, expires_at) VALUES ($1,$2, now() + interval '24 hours')`, id, verificationHash)
	if h.Mailer != nil {
		subject, html := h.Mailer.VerifyEmailEmail(req.Email, verificationToken)
		h.Mailer.SendAsync(req.Email, subject, html)
	}
	response := map[string]any{
		"user":                  map[string]any{"id": id, "name": req.Name, "email": req.Email},
		"email":                 req.Email,
		"verification_required": true,
	}
	if h.Svc.Cfg.Env == "development" {
		response["verification_token"] = verificationToken
	}
	// Integration tests use generated bearer tokens to exercise unrelated
	// domain handlers. Production and development never create a session here.
	if h.Svc.Cfg.Env == "test" {
		accessToken, tokenErr := GenerateAccessToken(h.Svc.Cfg, id)
		if tokenErr != nil {
			httpx.WriteError(w, r, tokenErr)
			return
		}
		rawRefresh, _ := GenerateRefreshToken()
		if tokenErr := h.Svc.CreateSession(r.Context(), id, rawRefresh, r.UserAgent(), r.RemoteAddr); tokenErr != nil {
			httpx.WriteError(w, r, tokenErr)
			return
		}
		response["access_token"] = accessToken
		response["refresh_token"] = rawRefresh
		response["token_type"] = "Bearer"
		response["expires_in"] = h.Svc.Cfg.JWTExpiryMinutes * 60
	}
	httpx.WriteJSON(w, 201, response)
}

type loginReq struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

func (h *Handler) Login(w http.ResponseWriter, r *http.Request) {
	var req loginReq
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteJSON(w, 400, map[string]any{"error": map[string]string{"code": "VALIDATION_ERROR", "message": err.Error()}})
		return
	}
	req.Email = strings.TrimSpace(strings.ToLower(req.Email))
	var id uuid.UUID
	var name, email, hash string
	var verifiedAt *string
	err := h.Svc.Pool.QueryRow(r.Context(),
		`SELECT id, name, email, password_hash, email_verified_at::text FROM users WHERE lower(email)=lower($1) LIMIT 1`, req.Email).Scan(&id, &name, &email, &hash, &verifiedAt)
	if err == pgx.ErrNoRows || !VerifyPassword(hash, req.Password) {
		httpx.WriteJSON(w, 401, map[string]any{"error": map[string]string{"code": "UNAUTHORIZED", "message": "invalid credentials"}})
		return
	}
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if verifiedAt == nil {
		httpx.WriteJSON(w, http.StatusForbidden, map[string]any{"error": map[string]string{
			"code":    "EMAIL_NOT_VERIFIED",
			"message": "Verify your email address before signing in.",
		}})
		return
	}
	accessToken, err := GenerateAccessToken(h.Svc.Cfg, id)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	rawRefresh, _ := GenerateRefreshToken()
	if err := h.Svc.CreateSession(r.Context(), id, rawRefresh, r.UserAgent(), r.RemoteAddr); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, 200, map[string]any{
		"user":          map[string]any{"id": id, "name": name, "email": email, "email_verified": true},
		"access_token":  accessToken,
		"refresh_token": rawRefresh,
		"token_type":    "Bearer",
		"expires_in":    h.Svc.Cfg.JWTExpiryMinutes * 60,
	})
}

type refreshReq struct {
	RefreshToken string `json:"refresh_token"`
}

func (h *Handler) Refresh(w http.ResponseWriter, r *http.Request) {
	var req refreshReq
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteJSON(w, 400, map[string]any{"error": map[string]string{"code": "VALIDATION_ERROR", "message": err.Error()}})
		return
	}
	newRaw, _ := GenerateRefreshToken()
	_, err := h.Svc.RotateSession(r.Context(), req.RefreshToken, newRaw)
	if err != nil {
		ae, ok := err.(*httpx.AppError)
		if ok {
			httpx.WriteError(w, r, ae)
			return
		}
		// Check for ErrUnauthorized returned as value
		if err.Error() == "authentication required" || strings.Contains(err.Error(), "unauthorized") {
			httpx.WriteError(w, r, httpx.ErrUnauthorized)
			return
		}
		httpx.WriteError(w, r, err)
		return
	}
	// Get user_id from new session
	var userID uuid.UUID
	_ = h.Svc.Pool.QueryRow(r.Context(), `SELECT user_id FROM sessions WHERE refresh_token_hash=$1`, HashToken(newRaw)).Scan(&userID)
	accessToken, err := GenerateAccessToken(h.Svc.Cfg, userID)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, 200, map[string]any{
		"access_token":  accessToken,
		"refresh_token": newRaw,
		"token_type":    "Bearer",
		"expires_in":    h.Svc.Cfg.JWTExpiryMinutes * 60,
	})
}

func (h *Handler) Logout(w http.ResponseWriter, r *http.Request) {
	var req refreshReq
	_ = json.NewDecoder(r.Body).Decode(&req)
	if req.RefreshToken != "" {
		_ = h.Svc.RevokeSession(r.Context(), req.RefreshToken)
	}
	httpx.WriteJSON(w, 200, map[string]any{"message": "logged out"})
}

type forgotReq struct {
	Email string `json:"email"`
}

func (h *Handler) ForgotPassword(w http.ResponseWriter, r *http.Request) {
	var req forgotReq
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteJSON(w, 400, map[string]any{"error": map[string]string{"code": "VALIDATION_ERROR", "message": err.Error()}})
		return
	}
	req.Email = strings.TrimSpace(strings.ToLower(req.Email))
	var userID uuid.UUID
	err := h.Svc.Pool.QueryRow(r.Context(), `SELECT id FROM users WHERE lower(email)=lower($1)`, req.Email).Scan(&userID)
	if err == pgx.ErrNoRows {
		// Do not reveal existence
		httpx.WriteJSON(w, 200, map[string]any{"message": "if the email exists, a reset link has been sent"})
		return
	}
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	raw, hash := GenerateRefreshToken()
	// Reuse refresh token random gen for reset tokens
	if _, err := h.Svc.Pool.Exec(r.Context(),
		`INSERT INTO password_reset_tokens (user_id, token_hash, expires_at) VALUES ($1,$2, now() + interval '1 hour')`,
		userID, hash); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if h.Mailer != nil {
		subject, html := h.Mailer.ResetPasswordEmail(req.Email, raw)
		h.Mailer.SendAsync(req.Email, subject, html)
	}
	// In dev, also return the token so tests and local dev work without email
	if h.Svc.Cfg.Env == "development" {
		httpx.WriteJSON(w, 200, map[string]any{"message": "reset token generated", "reset_token": raw})
		return
	}
	httpx.WriteJSON(w, 200, map[string]any{"message": "if the email exists, a reset link has been sent"})
}

type resetReq struct {
	Token       string `json:"token"`
	NewPassword string `json:"new_password"`
}

func (h *Handler) ResetPassword(w http.ResponseWriter, r *http.Request) {
	var req resetReq
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteJSON(w, 400, map[string]any{"error": map[string]string{"code": "VALIDATION_ERROR", "message": err.Error()}})
		return
	}
	if len(req.NewPassword) < 8 {
		httpx.WriteJSON(w, 422, map[string]any{"error": map[string]string{"code": "VALIDATION_ERROR", "message": "password must be >= 8 chars"}})
		return
	}
	hash := HashToken(req.Token)
	var userID uuid.UUID
	err := h.Svc.Pool.QueryRow(r.Context(),
		`SELECT user_id FROM password_reset_tokens WHERE token_hash=$1 AND expires_at > now() AND used_at IS NULL`, hash).Scan(&userID)
	if err == pgx.ErrNoRows {
		httpx.WriteJSON(w, 400, map[string]any{"error": map[string]string{"code": "VALIDATION_ERROR", "message": "invalid or expired token"}})
		return
	}
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	pwHash, err := HashPassword(req.NewPassword)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	tx, err := h.Svc.Pool.Begin(r.Context())
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	defer tx.Rollback(r.Context())
	if _, err = tx.Exec(r.Context(), `UPDATE users SET password_hash=$1, updated_at=now() WHERE id=$2`, pwHash, userID); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if _, err = tx.Exec(r.Context(), `UPDATE password_reset_tokens SET used_at=now() WHERE token_hash=$1`, hash); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if _, err = tx.Exec(r.Context(), `UPDATE sessions SET revoked_at=now() WHERE user_id=$1 AND revoked_at IS NULL`, userID); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, 200, map[string]any{"message": "password reset successful"})
}

func (h *Handler) VerifyEmail(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Token string `json:"token"`
	}
	if err := httpx.DecodeJSON(r, &req); err != nil || req.Token == "" {
		httpx.WriteJSON(w, 400, map[string]any{"error": map[string]string{"code": "VALIDATION_ERROR", "message": "token required"}})
		return
	}
	hash := HashToken(req.Token)
	var userID uuid.UUID
	err := h.Svc.Pool.QueryRow(r.Context(),
		`SELECT user_id FROM email_verification_tokens WHERE token_hash=$1 AND expires_at > now() AND verified_at IS NULL`, hash).Scan(&userID)
	if err == pgx.ErrNoRows {
		httpx.WriteJSON(w, 400, map[string]any{"error": map[string]string{"code": "VALIDATION_ERROR", "message": "invalid or expired token"}})
		return
	}
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	_, _ = h.Svc.Pool.Exec(r.Context(), `UPDATE users SET email_verified_at=now(), updated_at=now() WHERE id=$1`, userID)
	_, _ = h.Svc.Pool.Exec(r.Context(), `UPDATE email_verification_tokens SET verified_at=now() WHERE token_hash=$1`, hash)
	httpx.WriteJSON(w, 200, map[string]any{"message": "email verified"})
}

func (h *Handler) ResendVerification(w http.ResponseWriter, r *http.Request) {
	authHeader := r.Header.Get("Authorization")
	uid, err := h.Svc.GetUserIDFromToken(r.Context(), authHeader)
	var email string
	var verifiedAt *string
	if err == nil {
		_ = h.Svc.Pool.QueryRow(r.Context(),
			`SELECT email, email_verified_at::text FROM users WHERE id=$1`, uid).
			Scan(&email, &verifiedAt)
	} else {
		var req struct {
			Email string `json:"email"`
		}
		if decodeErr := httpx.DecodeJSON(r, &req); decodeErr != nil {
			httpx.WriteJSON(w, 400, map[string]any{"error": map[string]string{"code": "VALIDATION_ERROR", "message": "email required"}})
			return
		}
		req.Email = strings.TrimSpace(strings.ToLower(req.Email))
		if !emailRe.MatchString(req.Email) {
			httpx.WriteJSON(w, 422, map[string]any{"error": map[string]string{"code": "VALIDATION_ERROR", "message": "invalid email"}})
			return
		}
		// Keep this response non-enumerating: unknown and already verified
		// addresses receive the same success message.
		_ = h.Svc.Pool.QueryRow(r.Context(),
			`SELECT email, email_verified_at::text FROM users WHERE lower(email)=lower($1)`, req.Email).
			Scan(&email, &verifiedAt)
	}
	if email == "" || verifiedAt != nil {
		httpx.WriteJSON(w, 200, map[string]any{"message": "if verification is needed, an email has been sent"})
		return
	}
	raw, hash := GenerateRefreshToken()
	_, _ = h.Svc.Pool.Exec(r.Context(),
		`INSERT INTO email_verification_tokens (user_id, token_hash, expires_at) VALUES ($1,$2, now() + interval '24 hours')`, uid, hash)
	if h.Mailer != nil {
		subject, html := h.Mailer.VerifyEmailEmail(email, raw)
		h.Mailer.SendAsync(email, subject, html)
	}
	if h.Svc.Cfg.Env == "development" {
		httpx.WriteJSON(w, 200, map[string]any{"message": "verification email sent", "verification_token": raw})
		return
	}
	httpx.WriteJSON(w, 200, map[string]any{"message": "verification email sent"})
}

func (h *Handler) ListSessions(w http.ResponseWriter, r *http.Request) {
	uid, err := h.Svc.GetUserIDFromToken(r.Context(), r.Header.Get("Authorization"))
	if err != nil {
		httpx.WriteError(w, r, httpx.ErrUnauthorized)
		return
	}
	rows, err := h.Svc.Pool.Query(r.Context(),
		`SELECT id, user_agent, ip, created_at, last_used_at, expires_at, revoked_at FROM sessions WHERE user_id=$1 ORDER BY created_at DESC`, uid)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	defer rows.Close()
	var out []map[string]any
	for rows.Next() {
		var id uuid.UUID
		var ua, ip *string
		var createdAt, lastUsedAt, expiresAt any
		var revokedAt *string
		_ = rows.Scan(&id, &ua, &ip, &createdAt, &lastUsedAt, &expiresAt, &revokedAt)
		out = append(out, map[string]any{"id": id, "user_agent": ua, "ip": ip, "created_at": createdAt, "last_used_at": lastUsedAt, "expires_at": expiresAt, "revoked_at": revokedAt})
	}
	if out == nil {
		out = []map[string]any{}
	}
	httpx.WriteJSON(w, 200, map[string]any{"data": out})
}

func (h *Handler) RevokeSessionByID(w http.ResponseWriter, r *http.Request) {
	uid, err := h.Svc.GetUserIDFromToken(r.Context(), r.Header.Get("Authorization"))
	if err != nil {
		httpx.WriteError(w, r, httpx.ErrUnauthorized)
		return
	}
	sid, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		httpx.WriteJSON(w, 422, map[string]any{"error": map[string]string{"code": "VALIDATION_ERROR", "message": "invalid session id"}})
		return
	}
	res, _ := h.Svc.Pool.Exec(r.Context(), `UPDATE sessions SET revoked_at=now() WHERE id=$1 AND user_id=$2 AND revoked_at IS NULL`, sid, uid)
	if res.RowsAffected() == 0 {
		httpx.WriteError(w, r, httpx.ErrNotFound)
		return
	}
	httpx.WriteJSON(w, 200, map[string]any{"message": "session revoked"})
}

func (h *Handler) RevokeAllSessions(w http.ResponseWriter, r *http.Request) {
	uid, err := h.Svc.GetUserIDFromToken(r.Context(), r.Header.Get("Authorization"))
	if err != nil {
		httpx.WriteError(w, r, httpx.ErrUnauthorized)
		return
	}
	_ = h.Svc.RevokeAllSessions(r.Context(), uid)
	httpx.WriteJSON(w, 200, map[string]any{"message": "all sessions revoked"})
}
