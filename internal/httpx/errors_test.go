package httpx

import "testing"

func TestAppErrorCodes(t *testing.T) {
	if ErrUnauthorized.Code != "UNAUTHORIZED" {
		t.Fatal("code mismatch")
	}
	if ErrNotFound.Status != 404 {
		t.Fatal("status mismatch")
	}
	if ErrInvalidSplit.Code != "INVALID_SPLIT" {
		t.Fatal("invalid split code")
	}
}
