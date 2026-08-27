package config

import (
	"fmt"
	"os"
	"strings"

	"github.com/nabinkhanal00/settlr-api/internal/mailer"
)

type Config struct {
	Port              string
	DatabaseURL       string
	JWTSecret         string
	JWTRefreshSecret  string
	JWTExpiryMinutes  int
	RefreshExpiryDays int
	AppURL            string
	APIURL            string
	Env               string
	CORSOrigins       string
	Mail              mailer.Config
}

func Load() (Config, error) {
	cfg := Config{
		Port:              env("PORT", "8080"),
		DatabaseURL:       env("DATABASE_URL", "postgres://settlr:settlr@localhost:5432/settlr?sslmode=disable"),
		JWTSecret:         env("JWT_SECRET", "dev-jwt-secret-change-me-32chars!!"),
		JWTRefreshSecret:  env("JWT_REFRESH_SECRET", "dev-refresh-secret-change-me-32chars!!"),
		JWTExpiryMinutes:  15,
		RefreshExpiryDays: 30,
		AppURL:            env("APP_URL", "http://localhost:8081"),
		APIURL:            env("API_URL", "http://localhost:8080"),
		Env:               env("APP_ENV", "development"),
		CORSOrigins:       env("CORS_ORIGINS", "*"),
		Mail: mailer.Config{
			Provider:    env("MAIL_PROVIDER", ""),
			BrevoAPIKey: env("BREVO_API_KEY", ""),
			SMTPHost:    env("SMTP_HOST", ""),
			SMTPPort:    env("SMTP_PORT", "587"),
			SMTPUser:    env("SMTP_USER", ""),
			SMTPPass:    env("SMTP_PASS", ""),
			FromEmail:   env("MAIL_FROM_EMAIL", "noreply@settlr.app"),
			FromName:    env("MAIL_FROM_NAME", "Settlr"),
			AppURL:      env("APP_URL", "http://localhost:8081"),
		},
	}
	if strings.EqualFold(cfg.Env, "production") {
		if strings.Contains(cfg.JWTSecret, "dev-jwt-secret") || len(cfg.JWTSecret) < 32 {
			return Config{}, fmt.Errorf("JWT_SECRET must be set to a random value of at least 32 characters in production")
		}
		if strings.Contains(cfg.JWTRefreshSecret, "dev-refresh-secret") || len(cfg.JWTRefreshSecret) < 32 {
			return Config{}, fmt.Errorf("JWT_REFRESH_SECRET must be set to a random value of at least 32 characters in production")
		}
		if cfg.CORSOrigins == "*" || strings.TrimSpace(cfg.CORSOrigins) == "" {
			return Config{}, fmt.Errorf("CORS_ORIGINS must explicitly list allowed origins in production")
		}
	}
	return cfg, nil
}

func env(k, fallback string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return fallback
}
