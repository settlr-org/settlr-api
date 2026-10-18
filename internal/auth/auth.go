package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/argon2"

	"github.com/settlr-org/settlr-api/internal/config"
	db "github.com/settlr-org/settlr-api/internal/db"
	"github.com/settlr-org/settlr-api/internal/httpx"
)

// Supported currencies — 34 codes matching Spliit supportedCurrencyCodes
var SupportedCurrencies = map[string]bool{
	"USD": true, "EUR": true, "JPY": true, "BGN": true, "CZK": true, "DKK": true, "GBP": true, "HUF": true,
	"PLN": true, "RON": true, "SEK": true, "CHF": true, "ISK": true, "NOK": true, "TRY": true, "AUD": true,
	"BRL": true, "CAD": true, "CNY": true, "HKD": true, "IDR": true, "ILS": true, "INR": true, "KRW": true,
	"MKD": true, "MXN": true, "MYR": true, "NZD": true, "PHP": true, "SGD": true, "THB": true, "VND": true,
	"ZAR": true, "COP": true, "NPR": true,
}

func HashPassword(password string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	hash := argon2.IDKey([]byte(password), salt, 1, 64*1024, 4, 32)
	return fmt.Sprintf("%s$%s", hex.EncodeToString(salt), hex.EncodeToString(hash)), nil
}

func VerifyPassword(stored, password string) bool {
	parts := strings.Split(stored, "$")
	if len(parts) != 2 {
		return false
	}
	salt, err1 := hex.DecodeString(parts[0])
	expected, err2 := hex.DecodeString(parts[1])
	if err1 != nil || err2 != nil {
		return false
	}
	hash := argon2.IDKey([]byte(password), salt, 1, 64*1024, 4, 32)
	if len(hash) != len(expected) {
		return false
	}
	var diff byte
	for i := range hash {
		diff |= hash[i] ^ expected[i]
	}
	return diff == 0
}

func GenerateRefreshToken() (raw string, hash string) {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	raw = hex.EncodeToString(b)
	h := sha256.Sum256([]byte(raw))
	hash = hex.EncodeToString(h[:])
	return raw, hash
}

func HashToken(raw string) string {
	h := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(h[:])
}

// GenerateAccessToken mints an access token without a session marker.
func GenerateAccessToken(cfg config.Config, userID uuid.UUID) (string, error) {
	return GenerateSessionAccessToken(cfg, userID, uuid.Nil)
}

// GenerateSessionAccessToken mints an access token carrying `sid`, the id of the sessions row
// the accompanying refresh token belongs to. The claim only lets GET /auth/sessions mark the
// caller's own session as current; it is never used for authorization.
func GenerateSessionAccessToken(cfg config.Config, userID, sessionID uuid.UUID) (string, error) {
	now := time.Now()
	claims := jwt.MapClaims{
		"sub": userID.String(),
		"iat": now.Unix(),
		"exp": now.Add(time.Duration(cfg.JWTExpiryMinutes) * time.Minute).Unix(),
	}
	if sessionID != uuid.Nil {
		claims["sid"] = sessionID.String()
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return tok.SignedString([]byte(cfg.JWTSecret))
}

func ParseAccessToken(cfg config.Config, tokenStr string) (uuid.UUID, error) {
	id, _, err := ParseAccessClaims(cfg, tokenStr)
	return id, err
}

// ParseAccessClaims returns the user id and the session id claim (uuid.Nil when absent or malformed).
func ParseAccessClaims(cfg config.Config, tokenStr string) (uuid.UUID, uuid.UUID, error) {
	tok, err := jwt.Parse(tokenStr, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method")
		}
		return []byte(cfg.JWTSecret), nil
	})
	if err != nil || !tok.Valid {
		return uuid.Nil, uuid.Nil, fmt.Errorf("invalid token: %w", err)
	}
	claims, ok := tok.Claims.(jwt.MapClaims)
	if !ok {
		return uuid.Nil, uuid.Nil, fmt.Errorf("invalid claims")
	}
	sub, ok := claims["sub"].(string)
	if !ok {
		return uuid.Nil, uuid.Nil, fmt.Errorf("missing sub")
	}
	uid, err := uuid.Parse(sub)
	if err != nil {
		return uuid.Nil, uuid.Nil, err
	}
	sid := uuid.Nil
	if raw, ok := claims["sid"].(string); ok {
		sid, _ = uuid.Parse(raw)
	}
	return uid, sid, nil
}

// Service holds auth business logic.
// Pool is the raw pgx pool for manual queries (legacy).
// Queries is the sqlc-generated type-safe wrapper. New code should use Queries where possible;
// see internal/db/queries/auth.sql for migrated queries (CreateSession, RotateSession, etc.).
// Migration is incremental: methods use Queries when available, fallback to Pool when Queries == nil (tests without DB).
type Service struct {
	Pool    *pgxpool.Pool
	Queries *db.Queries
	Cfg     config.Config

	// denied holds users whose account was just deleted (uuid.UUID → expiry time.Time) so
	// their still-valid access tokens stop working immediately instead of at expiry.
	denied sync.Map
}

// DenyUser makes every access token of userID fail authentication until those tokens would
// have expired anyway (access-token lifetime plus a minute of clock-skew margin). Refresh
// already fails because the sessions are gone.
func (s *Service) DenyUser(userID uuid.UUID) {
	ttl := time.Duration(s.Cfg.JWTExpiryMinutes)*time.Minute + time.Minute
	if ttl < 2*time.Minute {
		ttl = 16 * time.Minute
	}
	s.denied.Store(userID, time.Now().Add(ttl))
}

func (s *Service) isDenied(userID uuid.UUID) bool {
	v, ok := s.denied.Load(userID)
	if !ok {
		return false
	}
	if time.Now().After(v.(time.Time)) {
		s.denied.Delete(userID)
		return false
	}
	return true
}

func (s *Service) ensureQueries() *db.Queries {
	if s.Queries != nil {
		return s.Queries
	}
	if s.Pool != nil {
		return db.New(s.Pool)
	}
	return nil
}

func (s *Service) GetUserIDFromToken(ctx context.Context, authHeader string) (uuid.UUID, error) {
	id, _, err := s.GetClaimsFromToken(ctx, authHeader)
	return id, err
}

// GetClaimsFromToken returns the user id and the session id claim (uuid.Nil for older tokens).
func (s *Service) GetClaimsFromToken(ctx context.Context, authHeader string) (uuid.UUID, uuid.UUID, error) {
	if authHeader == "" {
		return uuid.Nil, uuid.Nil, httpx.ErrUnauthorized
	}
	parts := strings.SplitN(authHeader, " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		return uuid.Nil, uuid.Nil, httpx.ErrUnauthorized
	}
	id, sid, err := ParseAccessClaims(s.Cfg, parts[1])
	if err != nil || s.isDenied(id) {
		return uuid.Nil, uuid.Nil, httpx.ErrUnauthorized
	}
	return id, sid, nil
}

// CreateSession stores a refresh token session.
func (s *Service) CreateSession(ctx context.Context, userID uuid.UUID, rawToken, userAgent, ip string) (uuid.UUID, error) {
	hash := HashToken(rawToken)
	expiresAt := time.Now().Add(time.Duration(s.Cfg.RefreshExpiryDays) * 24 * time.Hour)
	q := s.ensureQueries()
	if q == nil {
		return uuid.Nil, fmt.Errorf("no db queries available")
	}
	return q.CreateSession(ctx, db.CreateSessionParams{
		UserID:           userID,
		RefreshTokenHash: hash,
		UserAgent:        pgtype.Text{String: userAgent, Valid: userAgent != ""},
		Ip:               pgtype.Text{String: ip, Valid: ip != ""},
		ExpiresAt:        pgtype.Timestamptz{Time: expiresAt, Valid: true},
	})
}

func (s *Service) RotateSession(ctx context.Context, oldRaw, newRaw string) (uuid.UUID, error) {
	oldHash := HashToken(oldRaw)
	newHash := HashToken(newRaw)
	q := s.ensureQueries()
	if q == nil {
		return uuid.Nil, fmt.Errorf("no db queries available")
	}
	id, err := q.RotateSession(ctx, db.RotateSessionParams{
		RefreshTokenHash:   oldHash,
		RefreshTokenHash_2: newHash,
	})
	if err == pgx.ErrNoRows {
		return uuid.Nil, httpx.ErrUnauthorized
	}
	return id, err
}

func (s *Service) RevokeSession(ctx context.Context, rawToken string) error {
	q := s.ensureQueries()
	if q == nil {
		return fmt.Errorf("no db queries available")
	}
	return q.RevokeSessionByHash(ctx, HashToken(rawToken))
}

func (s *Service) RevokeAllSessions(ctx context.Context, userID uuid.UUID) error {
	q := s.ensureQueries()
	if q == nil {
		return fmt.Errorf("no db queries available")
	}
	return q.RevokeAllUserSessions(ctx, userID)
}
