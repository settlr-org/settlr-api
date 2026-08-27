package httpx

import (
	"net/http"
	"net/http/httptest"
	"testing"
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
