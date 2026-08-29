package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/argon2"

	"github.com/nabinkhanal00/settlr-api/internal/config"
	"github.com/nabinkhanal00/settlr-api/internal/httpx"
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

func GenerateAccessToken(cfg config.Config, userID uuid.UUID) (string, error) {
	now := time.Now()
	claims := jwt.MapClaims{
		"sub": userID.String(),
		"iat": now.Unix(),
		"exp": now.Add(time.Duration(cfg.JWTExpiryMinutes) * time.Minute).Unix(),
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return tok.SignedString([]byte(cfg.JWTSecret))
}

func ParseAccessToken(cfg config.Config, tokenStr string) (uuid.UUID, error) {
	tok, err := jwt.Parse(tokenStr, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method")
		}
		return []byte(cfg.JWTSecret), nil
	})
	if err != nil || !tok.Valid {
		return uuid.Nil, fmt.Errorf("invalid token: %w", err)
	}
	claims, ok := tok.Claims.(jwt.MapClaims)
	if !ok {
		return uuid.Nil, fmt.Errorf("invalid claims")
	}
	sub, ok := claims["sub"].(string)
	if !ok {
		return uuid.Nil, fmt.Errorf("missing sub")
	}
	return uuid.Parse(sub)
}

// Service holds auth business logic.
type Service struct {
	Pool *pgxpool.Pool
	Cfg  config.Config
}

func (s *Service) GetUserIDFromToken(ctx context.Context, authHeader string) (uuid.UUID, error) {
	if authHeader == "" {
		return uuid.Nil, httpx.ErrUnauthorized
	}
	parts := strings.SplitN(authHeader, " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		return uuid.Nil, httpx.ErrUnauthorized
	}
	id, err := ParseAccessToken(s.Cfg, parts[1])
	if err != nil {
		return uuid.Nil, httpx.ErrUnauthorized
	}
	return id, nil
}

// CreateSession stores a refresh token session.
func (s *Service) CreateSession(ctx context.Context, userID uuid.UUID, rawToken, userAgent, ip string) error {
	hash := HashToken(rawToken)
	expiresAt := time.Now().Add(time.Duration(s.Cfg.RefreshExpiryDays) * 24 * time.Hour)
	_, err := s.Pool.Exec(ctx,
		`INSERT INTO sessions (user_id, refresh_token_hash, user_agent, ip, expires_at) VALUES ($1,$2,$3,$4,$5)`,
		userID, hash, userAgent, ip, expiresAt)
	return err
}

func (s *Service) RotateSession(ctx context.Context, oldRaw, newRaw string) (uuid.UUID, error) {
	oldHash := HashToken(oldRaw)
	newHash := HashToken(newRaw)
	var newSessionID uuid.UUID
	err := s.Pool.QueryRow(ctx, `
		WITH old AS (
			UPDATE sessions SET revoked_at=now() WHERE refresh_token_hash=$1 AND revoked_at IS NULL RETURNING user_id, id
		)
		INSERT INTO sessions (user_id, refresh_token_hash, expires_at, rotated_from)
		SELECT user_id, $2, now() + interval '30 days', id FROM old
		RETURNING id
	`, oldHash, newHash).Scan(&newSessionID)
	if err == pgx.ErrNoRows {
		return uuid.Nil, httpx.ErrUnauthorized
	}
	return newSessionID, err
}

func (s *Service) RevokeSession(ctx context.Context, rawToken string) error {
	_, err := s.Pool.Exec(ctx, `UPDATE sessions SET revoked_at=now() WHERE refresh_token_hash=$1`, HashToken(rawToken))
	return err
}

func (s *Service) RevokeAllSessions(ctx context.Context, userID uuid.UUID) error {
	_, err := s.Pool.Exec(ctx, `UPDATE sessions SET revoked_at=now() WHERE user_id=$1 AND revoked_at IS NULL`, userID)
	return err
}
