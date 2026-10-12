package auth

import (
	"net/http"

	"github.com/settlr-org/settlr-api/internal/httpx"
	"github.com/settlr-org/settlr-api/internal/idempotency"
)

// Middleware authenticates the bearer token and puts the user id on the request
// context. Every route wrapped by it also gets Idempotency-Key handling for
// mutating methods (see internal/idempotency), since that needs the user id.
func Middleware(svc *Service) func(http.Handler) http.Handler {
	var guard *idempotency.Guard
	if q := svc.ensureQueries(); q != nil {
		guard = idempotency.New(q)
	}
	return func(next http.Handler) http.Handler {
		if guard != nil {
			next = guard.Wrap(next)
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			userID, err := svc.GetUserIDFromToken(r.Context(), r.Header.Get("Authorization"))
			if err != nil {
				httpx.WriteError(w, r, httpx.ErrUnauthorized)
				return
			}
			ctx := httpx.SetUserID(r.Context(), userID.String())
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}
