package auth

import (
	"net/http"

	"github.com/nabinkhanal00/settlr-api/internal/httpx"
)

func Middleware(svc *Service) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
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

// OptionalAuth does not require auth but populates user_id if token present.
func OptionalAuth(svc *Service) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if hdr := r.Header.Get("Authorization"); hdr != "" {
				if id, err := svc.GetUserIDFromToken(r.Context(), hdr); err == nil {
					r = r.WithContext(httpx.SetUserID(r.Context(), id.String()))
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}
