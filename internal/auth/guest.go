package auth

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	db "github.com/settlr-org/settlr-api/internal/db"
	"github.com/settlr-org/settlr-api/internal/httpx"
)

// Guest accounts let the mobile app work before anyone signs in: the wallet is fully usable, but group features
// (groups, friends, invitations, shared expenses) answer 403 GUEST_NOT_ALLOWED until the guest is upgraded to a
// real account with POST /auth/upgrade (e-mail + password, completed by verifying the address) or
// POST /auth/upgrade/google. The upgrade keeps the user id, so all wallet data carries over.

type guestEntry struct {
	guest   bool
	expires time.Time
}

// guestBlockedPrefixes are the route prefixes a guest cannot use.
var guestBlockedPrefixes = []string{
	"/api/v1/groups", "/api/v1/friends", "/api/v1/friend-invites", "/api/v1/invites", "/api/v1/users",
	"/api/v1/expenses", "/api/v1/settlements", "/api/v1/comments", "/api/v1/attachments", "/api/v1/recurring",
}

// IsGuest reports whether the user is still a guest. A "true" answer is cached for seconds (so an upgrade takes
// effect quickly), a "false" answer for minutes (an account never goes back to being a guest).
func (s *Service) IsGuest(ctx context.Context, id uuid.UUID) bool {
	if v, ok := s.guests.Load(id); ok {
		if e := v.(guestEntry); time.Now().Before(e.expires) {
			return e.guest
		}
	}
	q := s.ensureQueries()
	if q == nil {
		return false
	}
	guest, err := q.IsGuestUser(ctx, id)
	if err != nil {
		return false
	}
	ttl := 10 * time.Minute
	if guest {
		ttl = 15 * time.Second
	}
	s.guests.Store(id, guestEntry{guest: guest, expires: time.Now().Add(ttl)})
	return guest
}

// ForgetGuest drops the cached answer for a user (called when a guest is upgraded).
func (s *Service) ForgetGuest(id uuid.UUID) { s.guests.Delete(id) }

// BlockGuests wraps the whole API: requests from guests to group-related routes get 403. Requests without a
// valid token pass through so the route's own authentication answers 401.
func BlockGuests(svc *Service, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/settlr")
		for _, prefix := range guestBlockedPrefixes {
			if path == prefix || strings.HasPrefix(path, prefix+"/") {
				if id, err := svc.GetUserIDFromToken(r.Context(), r.Header.Get("Authorization")); err == nil && svc.IsGuest(r.Context(), id) {
					httpx.WriteJSON(w, http.StatusForbidden, map[string]any{"error": map[string]string{
						"code": "GUEST_NOT_ALLOWED", "message": "Sign in to use groups and friends.",
					}})
					return
				}
				break
			}
		}
		next.ServeHTTP(w, r)
	})
}

func guestError(w http.ResponseWriter, status int, code, message string) {
	httpx.WriteJSON(w, status, map[string]any{"error": map[string]string{"code": code, "message": message}})
}

type guestReq struct {
	Currency string `json:"currency"`
}

// CreateGuest handles POST /auth/guest: it creates an anonymous account and a session for it.
func (h *Handler) CreateGuest(w http.ResponseWriter, r *http.Request) {
	var req guestReq
	_ = httpx.DecodeJSON(r, &req) // the body is optional
	currency := strings.ToUpper(strings.TrimSpace(req.Currency))
	if currency == "" {
		currency = "USD"
	}
	if !SupportedCurrencies[currency] {
		guestError(w, 422, "VALIDATION_ERROR", "unsupported currency")
		return
	}
	id := uuid.New()
	email := "guest-" + id.String() + "@guest.invalid"
	if err := h.ensureQueries().CreateGuestUser(r.Context(), db.CreateGuestUserParams{ID: id, Email: email, DefaultCurrency: currency}); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	h.writeGuestSession(w, r, id, "Guest", email, true, http.StatusCreated)
}

func (h *Handler) writeGuestSession(w http.ResponseWriter, r *http.Request, id uuid.UUID, name, email string, guest bool, status int) {
	rawRefresh, _ := GenerateRefreshToken()
	sid, err := h.Svc.CreateSession(r.Context(), id, rawRefresh, r.UserAgent(), r.RemoteAddr)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	accessToken, err := GenerateSessionAccessToken(h.Svc.Cfg, id, sid)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, status, map[string]any{
		"user":          map[string]any{"id": id, "name": name, "email": email, "email_verified": !guest, "is_guest": guest},
		"access_token":  accessToken,
		"refresh_token": rawRefresh,
		"token_type":    "Bearer",
		"expires_in":    h.Svc.Cfg.JWTExpiryMinutes * 60,
	})
}

// UpgradeGuest handles POST /auth/upgrade: attach an e-mail address and password to the signed-in guest. The
// account stays a guest until the address is verified (POST /auth/verify-email), which keeps invitations tied to
// addresses their owner has proven.
func (h *Handler) UpgradeGuest(w http.ResponseWriter, r *http.Request) {
	uid, _ := httpx.GetUserID(r.Context())
	id, err := uuid.Parse(uid)
	if err != nil {
		httpx.WriteError(w, r, httpx.ErrUnauthorized)
		return
	}
	var req registerReq
	if err := httpx.DecodeJSON(r, &req); err != nil {
		guestError(w, 400, "VALIDATION_ERROR", err.Error())
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	req.Email = strings.TrimSpace(strings.ToLower(req.Email))
	if req.Name == "" || len(req.Name) > 100 {
		guestError(w, 422, "VALIDATION_ERROR", "name is required (max 100 chars)")
		return
	}
	if !emailRe.MatchString(req.Email) || strings.HasSuffix(req.Email, "@guest.invalid") {
		guestError(w, 422, "VALIDATION_ERROR", "invalid email")
		return
	}
	if len(req.Password) < 8 || len(req.Password) > 128 {
		guestError(w, 422, "VALIDATION_ERROR", "password must be 8-128 characters")
		return
	}
	q := h.ensureQueries()
	if !h.Svc.IsGuest(r.Context(), id) {
		guestError(w, 409, "NOT_GUEST", "this account is already a full account")
		return
	}
	hash, err := HashPassword(req.Password)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	n, err := q.StartGuestUpgrade(r.Context(), db.StartGuestUpgradeParams{
		ID: id, Name: req.Name, Email: req.Email, PasswordHash: pgtype.Text{String: hash, Valid: true},
	})
	if err != nil && strings.Contains(err.Error(), "duplicate") {
		guestError(w, 409, "CONFLICT", "email already registered")
		return
	}
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if n == 0 {
		guestError(w, 409, "NOT_GUEST", "this account is already a full account")
		return
	}
	token, tokenHash := GenerateRefreshToken()
	_ = q.CreateEmailVerificationToken(r.Context(), db.CreateEmailVerificationTokenParams{UserID: id, TokenHash: tokenHash})
	if h.Mailer != nil {
		subject, html := h.Mailer.VerifyEmailEmail(req.Email, token)
		h.Mailer.SendAsync(req.Email, subject, html)
	}
	resp := map[string]any{"email": req.Email, "verification_required": true}
	if h.Svc.Cfg.Env == "development" {
		resp["verification_token"] = token
	}
	httpx.WriteJSON(w, http.StatusAccepted, resp)
}

// UpgradeGuestGoogle handles POST /auth/upgrade/google: link a Google identity to the signed-in guest. It fails
// with 409 ACCOUNT_EXISTS when that Google account or its e-mail already belongs to a Settlr account (the guest's
// data is not merged into it; the app offers to sign in there instead).
func (h *Handler) UpgradeGuestGoogle(w http.ResponseWriter, r *http.Request) {
	uid, _ := httpx.GetUserID(r.Context())
	id, err := uuid.Parse(uid)
	if err != nil {
		httpx.WriteError(w, r, httpx.ErrUnauthorized)
		return
	}
	var req googleLoginReq
	if err := httpx.DecodeJSON(r, &req); err != nil || strings.TrimSpace(req.IDToken) == "" {
		guestError(w, 400, "VALIDATION_ERROR", "Google ID token is required")
		return
	}
	if !h.Svc.IsGuest(r.Context(), id) {
		guestError(w, 409, "NOT_GUEST", "this account is already a full account")
		return
	}
	identity, err := VerifyGoogleIDToken(r.Context(), req.IDToken, h.Svc.Cfg.GoogleOAuthClientIDs)
	if err != nil {
		status, code, message := http.StatusUnauthorized, "UNAUTHORIZED", "Google sign-in could not be verified"
		if strings.Contains(err.Error(), "not configured") {
			status, code, message = http.StatusServiceUnavailable, "OAUTH_NOT_CONFIGURED", "Google sign-in is not configured"
		}
		guestError(w, status, code, message)
		return
	}
	tx, err := h.Svc.Pool.Begin(r.Context())
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	defer tx.Rollback(r.Context()) //nolint:errcheck // no-op after commit
	qtx := h.qtx(tx)
	if _, err := qtx.GetOAuthIdentityForUpdate(r.Context(), identity.Subject); err == nil {
		guestError(w, 409, "ACCOUNT_EXISTS", "that Google account already has a Settlr account")
		return
	} else if err != pgx.ErrNoRows {
		httpx.WriteError(w, r, err)
		return
	}
	if _, err := qtx.GetUserByEmailForUpdate(r.Context(), identity.Email); err == nil {
		guestError(w, 409, "ACCOUNT_EXISTS", "that e-mail already has a Settlr account")
		return
	} else if err != pgx.ErrNoRows {
		httpx.WriteError(w, r, err)
		return
	}
	name := identity.Name
	if name == "" {
		name = strings.Split(identity.Email, "@")[0]
	}
	n, err := qtx.CompleteGuestUpgradeGoogle(r.Context(), db.CompleteGuestUpgradeGoogleParams{ID: id, Name: name, Email: identity.Email})
	if err == nil && n == 0 {
		guestError(w, 409, "NOT_GUEST", "this account is already a full account")
		return
	}
	if err == nil {
		err = qtx.CreateOAuthIdentity(r.Context(), db.CreateOAuthIdentityParams{Subject: identity.Subject, UserID: id})
	}
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	h.Svc.ForgetGuest(id)
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"user": map[string]any{"id": id, "name": name, "email": identity.Email, "email_verified": true, "is_guest": false}})
}
