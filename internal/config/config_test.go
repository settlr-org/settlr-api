package config

import "testing"

func TestLoadRejectsInsecureProductionDefaults(t *testing.T) {
	t.Setenv("APP_ENV", "production")
	t.Setenv("JWT_SECRET", "")
	t.Setenv("JWT_REFRESH_SECRET", "")
	t.Setenv("CORS_ORIGINS", "")

	if _, err := Load(); err == nil {
		t.Fatal("Load accepted insecure production defaults")
	}
}

func TestLoadAcceptsExplicitProductionSecuritySettings(t *testing.T) {
	t.Setenv("APP_ENV", "production")
	t.Setenv("JWT_SECRET", "a-secure-production-secret-that-is-long-enough")
	t.Setenv("JWT_REFRESH_SECRET", "another-secure-production-secret-long-enough")
	t.Setenv("CORS_ORIGINS", "https://app.example.com")

	if _, err := Load(); err != nil {
		t.Fatalf("Load rejected explicit production settings: %v", err)
	}
}
