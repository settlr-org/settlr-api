package auth

import (
	"context"
	"fmt"
	"strings"

	"google.golang.org/api/idtoken"
)

type googleIdentity struct {
	Subject string
	Email   string
	Name    string
}

// VerifyGoogleIDToken uses Google's maintained verifier, which validates the
// JWT signature, issuer, expiry and requested audience against Google's keys.
func VerifyGoogleIDToken(ctx context.Context, token, configuredAudiences string) (googleIdentity, error) {
	audiences := splitValues(configuredAudiences)
	if len(audiences) == 0 {
		return googleIdentity{}, fmt.Errorf("Google sign-in is not configured")
	}
	var payload *idtoken.Payload
	var err error
	for _, audience := range audiences {
		payload, err = idtoken.Validate(ctx, token, audience)
		if err == nil {
			break
		}
	}
	if err != nil || payload == nil {
		return googleIdentity{}, fmt.Errorf("invalid Google token")
	}
	email, _ := payload.Claims["email"].(string)
	emailVerified, _ := payload.Claims["email_verified"].(bool)
	name, _ := payload.Claims["name"].(string)
	if payload.Subject == "" || !emailVerified || strings.TrimSpace(email) == "" {
		return googleIdentity{}, fmt.Errorf("invalid Google token")
	}
	return googleIdentity{
		Subject: payload.Subject,
		Email:   strings.ToLower(strings.TrimSpace(email)),
		Name:    strings.TrimSpace(name),
	}, nil
}

func splitValues(value string) []string {
	var values []string
	for _, part := range strings.Split(value, ",") {
		if part = strings.TrimSpace(part); part != "" {
			values = append(values, part)
		}
	}
	return values
}
