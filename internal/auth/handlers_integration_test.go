package auth

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/nabinkhanal00/settlr-api/internal/config"
	"github.com/nabinkhanal00/settlr-api/internal/testutil"
)

func TestRegistrationRequiresEmailVerificationBeforeLogin(t *testing.T) {
	pool := testutil.CleanDB(t)
	handler := &Handler{Svc: &Service{Pool: pool, Cfg: config.Config{
		JWTSecret:         "test-jwt-secret-32-chars-min!!!!",
		JWTRefreshSecret:  "test-refresh-secret-32-chars!!",
		JWTExpiryMinutes:  15,
		RefreshExpiryDays: 30,
		Env:               "development",
	}}}
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)
	server := httptest.NewServer(mux)
	defer server.Close()

	credentials := map[string]string{
		"name": "Pending User", "email": "pending@test.local", "password": "password123",
	}
	loginCredentials := map[string]string{
		"email": "pending@test.local", "password": "password123",
	}
	registration := postJSON(t, server.URL+"/api/v1/auth/register", credentials)
	if registration.status != http.StatusCreated {
		t.Fatalf("register status = %d", registration.status)
	}
	if registration.body["access_token"] != nil || registration.body["verification_required"] != true {
		t.Fatalf("unexpected registration response: %#v", registration.body)
	}

	login := postJSON(t, server.URL+"/api/v1/auth/login", loginCredentials)
	if login.status != http.StatusForbidden {
		t.Fatalf("unverified login status = %d, want 403", login.status)
	}

	verification := postJSON(t, server.URL+"/api/v1/auth/verify-email", map[string]string{
		"token": registration.body["verification_token"].(string),
	})
	if verification.status != http.StatusOK {
		t.Fatalf("verify status = %d", verification.status)
	}

	login = postJSON(t, server.URL+"/api/v1/auth/login", loginCredentials)
	if login.status != http.StatusOK || login.body["access_token"] == nil {
		t.Fatalf("verified login failed: %d %#v", login.status, login.body)
	}
}

type jsonResponse struct {
	status int
	body   map[string]any
}

func postJSON(t *testing.T, url string, input any) jsonResponse {
	t.Helper()
	payload, _ := json.Marshal(input)
	response, err := http.Post(url, "application/json", bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var body map[string]any
	_ = json.NewDecoder(response.Body).Decode(&body)
	return jsonResponse{status: response.StatusCode, body: body}
}
