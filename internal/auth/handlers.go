package auth

import (
	"encoding/json"
	"net/http"
	"regexp"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	db "github.com/settlr-org/settlr-api/internal/db"
	"github.com/settlr-org/settlr-api/internal/httpx"
	"github.com/settlr-org/settlr-api/internal/mailer"
)

var emailRe = regexp.MustCompile(`^[^@\s]+@[^@\s]+\.[^@\s]+$`)

type Handler struct {
	Svc     *Service
	Mailer  *mailer.Mailer
	Queries *db.Queries
}

func (h *Handler) ensureQueries() *db.Queries {
	if h.Queries != nil {
		return h.Queries
	}
	if h.Svc != nil {
		if q := h.Svc.ensureQueries(); q != nil {
			return q
		}
		if h.Svc.Pool != nil {
			return db.New(h.Svc.Pool)
		}
	}
	return nil
}

func (h *Handler) qtx(tx pgx.Tx) *db.Queries {
	if h.Queries != nil {
		return h.Queries.WithTx(tx)
	}
	if h.Svc != nil && h.Svc.Queries != nil {
		return h.Svc.Queries.WithTx(tx)
	}
	return db.New(tx)
}

func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/auth/register", h.Register)
	mux.HandleFunc("POST /api/v1/auth/login", h.Login)
	mux.HandleFunc("POST /api/v1/auth/google", h.GoogleLogin)
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

type googleLoginReq struct {
	IDToken string `json:"id_token"`
}

// GoogleLogin accepts an ID token from a platform-specific Google OAuth client.
// A verified Google email links to the existing Settlr account with that email;
// otherwise it creates a verified, passwordless account.
func (h *Handler) GoogleLogin(w http.ResponseWriter, r *http.Request) {
	var req googleLoginReq
	if err := httpx.DecodeJSON(r, &req); err != nil || strings.TrimSpace(req.IDToken) == "" {
		httpx.WriteJSON(w, http.StatusBadRequest, map[string]any{"error": map[string]string{"code": "VALIDATION_ERROR", "message": "Google ID token is required"}})
		return
	}
	identity, err := VerifyGoogleIDToken(r.Context(), req.IDToken, h.Svc.Cfg.GoogleOAuthClientIDs)
	if err != nil {
		status, code, message := http.StatusUnauthorized, "UNAUTHORIZED", "Google sign-in could not be verified"
		if strings.Contains(err.Error(), "not configured") {
			status, code, message = http.StatusServiceUnavailable, "OAUTH_NOT_CONFIGURED", "Google sign-in is not configured"
		}
		httpx.WriteJSON(w, status, map[string]any{"error": map[string]string{"code": code, "message": message}})
		return
	}

	tx, err := h.Svc.Pool.Begin(r.Context())
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	defer tx.Rollback(r.Context())
	qtx := h.qtx(tx)
	var id uuid.UUID
	var name, email string
	row, err := qtx.GetOAuthIdentityForUpdate(r.Context(), identity.Subject)
	if err == nil {
		id = row.ID
		name = row.Name
		email = row.Email
	} else if err == pgx.ErrNoRows {
		uRow, err2 := qtx.GetUserByEmailForUpdate(r.Context(), identity.Email)
		if err2 == pgx.ErrNoRows {
			id = uuid.New()
			name = identity.Name
			if name == "" {
				name = strings.Split(identity.Email, "@")[0]
			}
			email = identity.Email
			err = qtx.CreateOAuthUser(r.Context(), db.CreateOAuthUserParams{ID: id, Name: name, Email: email})
		} else if err2 != nil {
			httpx.WriteError(w, r, err2)
			return
		} else {
			id = uRow.ID
			name = uRow.Name
			email = uRow.Email
		}
		if err == nil {
			err = qtx.CreateOAuthIdentity(r.Context(), db.CreateOAuthIdentityParams{Subject: identity.Subject, UserID: id})
		}
		if err == nil {
			err = qtx.SetUserEmailVerifiedIfNull(r.Context(), id)
		}
	}
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	h.writeSession(w, r, id, name, email)
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
	q := h.ensureQueries()
	err = q.CreateUser(r.Context(), db.CreateUserParams{
		ID:           id,
		Name:         req.Name,
		Email:        req.Email,
		PasswordHash: pgtype.Text{String: hash, Valid: true},
	})
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
	_ = q.CreateEmailVerificationToken(r.Context(), db.CreateEmailVerificationTokenParams{UserID: id, TokenHash: verificationHash})
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
	q := h.ensureQueries()
	var id uuid.UUID
	var name, email string
	var hashValid bool
	var hashStr string
	var verifiedValid bool
	row, err := q.GetUserByEmailForLogin(r.Context(), req.Email)
	if err == pgx.ErrNoRows {
		httpx.WriteJSON(w, 401, map[string]any{"error": map[string]string{"code": "UNAUTHORIZED", "message": "invalid credentials"}})
		return
	}
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	id = row.ID
	name = row.Name
	email = row.Email
	hashValid = row.PasswordHash.Valid
	hashStr = row.PasswordHash.String
	verifiedValid = row.EmailVerifiedAt.Valid
	if !hashValid || !VerifyPassword(hashStr, req.Password) {
		httpx.WriteJSON(w, 401, map[string]any{"error": map[string]string{"code": "UNAUTHORIZED", "message": "invalid credentials"}})
		return
	}
	if !verifiedValid {
		httpx.WriteJSON(w, http.StatusForbidden, map[string]any{"error": map[string]string{
			"code":    "EMAIL_NOT_VERIFIED",
			"message": "Verify your email address before signing in.",
		}})
		return
	}
	h.writeSession(w, r, id, name, email)
}

func (h *Handler) writeSession(w http.ResponseWriter, r *http.Request, id uuid.UUID, name, email string) {
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
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
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
	q := h.ensureQueries()
	userID, _ = q.GetSessionUserByHash(r.Context(), HashToken(newRaw))
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
	q := h.ensureQueries()
	var userID uuid.UUID
	userID, err := q.GetUserIDByEmail(r.Context(), req.Email)
	if err == pgx.ErrNoRows {
		httpx.WriteJSON(w, 200, map[string]any{"message": "if the email exists, a reset link has been sent"})
		return
	}
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	raw, hash := GenerateRefreshToken()
	// Reuse refresh token random gen for reset tokens
	if err := q.CreatePasswordResetToken(r.Context(), db.CreatePasswordResetTokenParams{UserID: userID, TokenHash: hash}); err != nil {
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
	q := h.ensureQueries()
	var userID uuid.UUID
	userID, err := q.GetPasswordResetTokenUser(r.Context(), hash)
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
	qtx := h.qtx(tx)
	if err = qtx.UpdateUserPassword(r.Context(), db.UpdateUserPasswordParams{PasswordHash: pgtype.Text{String: pwHash, Valid: true}, ID: userID}); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if err = qtx.MarkPasswordResetUsed(r.Context(), hash); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if err = qtx.RevokeAllUserSessions(r.Context(), userID); err != nil {
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
	q := h.ensureQueries()
	var userID uuid.UUID
	userID, err := q.GetEmailVerificationTokenUser(r.Context(), hash)
	if err == pgx.ErrNoRows {
		httpx.WriteJSON(w, 400, map[string]any{"error": map[string]string{"code": "VALIDATION_ERROR", "message": "invalid or expired token"}})
		return
	}
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	_ = q.SetEmailVerified(r.Context(), userID)
	_ = q.MarkEmailVerificationVerified(r.Context(), hash)
	httpx.WriteJSON(w, 200, map[string]any{"message": "email verified"})
}

func (h *Handler) ResendVerification(w http.ResponseWriter, r *http.Request) {
	authHeader := r.Header.Get("Authorization")
	uid, err := h.Svc.GetUserIDFromToken(r.Context(), authHeader)
	var email string
	var verifiedValid bool
	q := h.ensureQueries()
	if err == nil {
		row, qerr := q.GetUserVerificationByID(r.Context(), uid)
		if qerr == nil {
			email = row.Email
			verifiedValid = row.EmailVerifiedAt.Valid
		}
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
		row, qerr := q.GetUserVerificationByEmail(r.Context(), req.Email)
		if qerr == nil {
			email = row.Email
			verifiedValid = row.EmailVerifiedAt.Valid
			// Need to get uid for token insertion: lookup user id by email
			if uid == uuid.Nil {
				if id, ierr := q.GetUserIDByEmail(r.Context(), req.Email); ierr == nil {
					uid = id
				}
			}
		}
	}
	if email == "" || verifiedValid {
		httpx.WriteJSON(w, 200, map[string]any{"message": "if verification is needed, an email has been sent"})
		return
	}
	raw, hash := GenerateRefreshToken()
	_ = q.CreateEmailVerificationToken(r.Context(), db.CreateEmailVerificationTokenParams{UserID: uid, TokenHash: hash})
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
	q := h.ensureQueries()
	rows, err := q.ListSessionsByUser(r.Context(), uid)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	out := make([]map[string]any, 0, len(rows))
	for _, s := range rows {
		var ua, ip any
		if s.UserAgent.Valid {
			ua = s.UserAgent.String
		}
		if s.Ip.Valid {
			ip = s.Ip.String
		}
		var revokedAt any
		if s.RevokedAt.Valid {
			revokedAt = s.RevokedAt.Time
		}
		out = append(out, map[string]any{"id": s.ID, "user_agent": ua, "ip": ip, "created_at": s.CreatedAt.Time, "last_used_at": s.LastUsedAt.Time, "expires_at": s.ExpiresAt.Time, "revoked_at": revokedAt})
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
	q := h.ensureQueries()
	_, err = q.RevokeSessionByIDReturning(r.Context(), db.RevokeSessionByIDReturningParams{ID: sid, UserID: uid})
	if err == pgx.ErrNoRows {
		httpx.WriteError(w, r, httpx.ErrNotFound)
		return
	}
	if err != nil {
		httpx.WriteError(w, r, err)
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
