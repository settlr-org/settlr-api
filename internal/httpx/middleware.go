package httpx

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// RequestID middleware injects X-Request-ID and stores it in context.
func RequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Request-ID")
		if id == "" {
			var b [16]byte
			_, _ = rand.Read(b[:])
			id = hex.EncodeToString(b[:])
		}
		w.Header().Set("X-Request-ID", id)
		ctx := context.WithValue(r.Context(), ctxKeyRequestID, id)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

type ctxKey string

const ctxKeyRequestID ctxKey = "request_id"
const ctxKeyUserID ctxKey = "user_id"

// Logger logs method, path, status, duration, request_id.
func Logger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rw := &responseWriter{ResponseWriter: w, status: 200}
		next.ServeHTTP(rw, r)
		dur := time.Since(start)
		rid, _ := r.Context().Value(ctxKeyRequestID).(string)
		slog.Info("request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", rw.status,
			"duration_ms", dur.Milliseconds(),
			"request_id", rid,
		)
	})
}

type responseWriter struct {
	http.ResponseWriter
	status int
}

func (rw *responseWriter) WriteHeader(code int) {
	rw.status = code
	rw.ResponseWriter.WriteHeader(code)
}

// Recover catches panics and returns 500.
func Recover(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				slog.Error("panic recovered", "panic", rec, "path", r.URL.Path)
				WriteJSON(w, http.StatusInternalServerError, map[string]any{"error": map[string]string{"code": "INTERNAL", "message": "internal server error"}})
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// CORS allows configured origins (permissive in dev).
func CORS(allowedOrigins string) func(http.Handler) http.Handler {
	origins := make(map[string]struct{})
	for _, value := range strings.Split(allowedOrigins, ",") {
		if origin := strings.TrimSpace(value); origin != "" {
			origins[origin] = struct{}{}
		}
	}
	allowAll := allowedOrigins == "" || allowedOrigins == "*"
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")
			if allowAll {
				w.Header().Set("Access-Control-Allow-Origin", "*")
			} else if _, ok := origins[origin]; ok {
				w.Header().Set("Access-Control-Allow-Origin", origin)
				w.Header().Set("Access-Control-Allow-Credentials", "true")
				w.Header().Add("Vary", "Origin")
			}
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PATCH, PUT, DELETE, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-Request-ID")
			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// SecurityHeaders sets common security headers.
func SecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
		next.ServeHTTP(w, r)
	})
}

// RequestSizeLimit limits body size to 1MB.
func RequestSizeLimit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		next.ServeHTTP(w, r)
	})
}

// Simple in-memory rate limiter (token bucket per key).
type RateLimiter struct {
	mu      sync.Mutex
	buckets map[string]*bucket
	limit   int
	window  time.Duration
}

type bucket struct {
	count int
	reset time.Time
}

func NewRateLimiter(limit int, window time.Duration) *RateLimiter {
	return &RateLimiter{buckets: make(map[string]*bucket), limit: limit, window: window}
}

func (rl *RateLimiter) Allow(key string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	now := time.Now()
	// Bound memory in a long-running process. Entries are only useful until
	// their window expires, so prune them while holding the existing lock.
	for bucketKey, existing := range rl.buckets {
		if now.After(existing.reset) {
			delete(rl.buckets, bucketKey)
		}
	}
	b, ok := rl.buckets[key]
	if !ok || now.After(b.reset) {
		rl.buckets[key] = &bucket{count: 1, reset: now.Add(rl.window)}
		return true
	}
	if b.count >= rl.limit {
		return false
	}
	b.count++
	return true
}

// ClientIP returns a normalized client IP. Forwarded headers are trusted only
// when the deployment explicitly opts in after putting the API behind a proxy.
func ClientIP(r *http.Request, trustProxyHeaders bool) string {
	if trustProxyHeaders {
		if forwarded := r.Header.Get("X-Forwarded-For"); forwarded != "" {
			if ip := strings.TrimSpace(strings.Split(forwarded, ",")[0]); net.ParseIP(ip) != nil {
				return ip
			}
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil && net.ParseIP(host) != nil {
		return host
	}
	return r.RemoteAddr
}

// GetUserID returns authenticated user ID from context, if present.
func GetUserID(ctx context.Context) (string, bool) {
	v, ok := ctx.Value(ctxKeyUserID).(string)
	return v, ok
}

func SetUserID(ctx context.Context, userID string) context.Context {
	return context.WithValue(ctx, ctxKeyUserID, userID)
}
