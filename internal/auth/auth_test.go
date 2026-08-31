package auth

import "testing"

func TestHashAndVerify(t *testing.T) {
	pw := "demo-password-123"
	hash, err := HashPassword(pw)
	if err != nil {
		t.Fatalf("hash error: %v", err)
	}
	if !VerifyPassword(hash, pw) {
		t.Fatal("verify failed")
	}
	if VerifyPassword(hash, "wrong") {
		t.Fatal("should not verify wrong password")
	}
}

func TestRefreshTokenHash(t *testing.T) {
	raw, hash := GenerateRefreshToken()
	if HashToken(raw) != hash {
		t.Fatal("hash mismatch")
	}
	raw2, _ := GenerateRefreshToken()
	if raw == raw2 {
		t.Fatal("tokens should differ")
	}
}
