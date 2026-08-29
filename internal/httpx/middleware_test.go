package httpx

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestCORSWildcardDoesNotEnableCredentials(t *testing.T) {
	h := CORS("*")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("Origin", "https://example.test")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "*" {
		t.Fatalf("origin = %q", got)
	}
	if got := w.Header().Get("Access-Control-Allow-Credentials"); got != "" {
		t.Fatalf("credentials = %q", got)
	}
}

func TestClientIPDoesNotTrustForwardedHeaderByDefault(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = "192.0.2.10:4321"
	r.Header.Set("X-Forwarded-For", "203.0.113.9")
	if got := ClientIP(r, false); got != "192.0.2.10" {
		t.Fatalf("client IP = %q", got)
	}
	if got := ClientIP(r, true); got != "203.0.113.9" {
		t.Fatalf("forwarded client IP = %q", got)
	}
}

func TestRateLimiterPrunesExpiredBuckets(t *testing.T) {
	rl := NewRateLimiter(1, time.Nanosecond)
	if !rl.Allow("first") {
		t.Fatal("first request should be allowed")
	}
	time.Sleep(time.Millisecond)
	if !rl.Allow("second") {
		t.Fatal("second request should be allowed")
	}
	if len(rl.buckets) != 1 {
		t.Fatalf("bucket count = %d, want 1", len(rl.buckets))
	}
}

func TestCORSEnabledOriginAllowsCredentials(t *testing.T) {
	h := CORS("https://settlr.theswissknife.com")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	r := httptest.NewRequest(http.MethodOptions, "/", nil)
	r.Header.Set("Origin", "https://settlr.theswissknife.com")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if got := w.Header().Get("Access-Control-Allow-Origin"); got != r.Header.Get("Origin") {
		t.Fatalf("origin = %q", got)
	}
	if got := w.Header().Get("Access-Control-Allow-Credentials"); got != "true" {
		t.Fatalf("credentials = %q", got)
	}
}
