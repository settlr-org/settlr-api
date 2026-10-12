// Package idempotency implements Idempotency-Key handling for authenticated
// mutating requests, so clients (notably the Android offline outbox) can retry
// writes without creating duplicates.
//
// For a POST/PUT/PATCH/DELETE carrying an Idempotency-Key header the first
// request atomically reserves (user id, key) together with a fingerprint of the
// request (method, path and a SHA-256 of the body), runs the handler while
// capturing its status, content type and body, and stores that response. A
// retry with the same key and fingerprint inside the TTL replays the stored
// response with "Idempotent-Replayed: true". A retry while the first request
// is still running gets 409 IDEMPOTENCY_IN_PROGRESS; reusing a key for a
// different request gets 422 IDEMPOTENCY_KEY_REUSED. 5xx responses and panics
// release the reservation so the retry runs again.
package idempotency

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	db "github.com/settlr-org/settlr-api/internal/db"
	"github.com/settlr-org/settlr-api/internal/httpx"
)

const (
	// HeaderKey is the request header carrying the client-generated key.
	HeaderKey = "Idempotency-Key"
	// HeaderReplayed is set on responses served from the idempotency store.
	HeaderReplayed = "Idempotent-Replayed"

	// DefaultTTL is how long a completed response is kept for replay.
	DefaultTTL = 24 * time.Hour
	// DefaultLease is how long a pending reservation blocks retries before it
	// is considered abandoned (e.g. the process died mid-request) and can be
	// taken over. It comfortably exceeds the server's 30s write timeout.
	DefaultLease = 2 * time.Minute

	maxKeyLen = 128
)

var (
	ErrInvalidKey = &httpx.AppError{Code: "IDEMPOTENCY_KEY_INVALID", Message: "Idempotency-Key must be 1-128 visible ASCII characters", Status: http.StatusBadRequest}
	ErrInProgress = &httpx.AppError{Code: "IDEMPOTENCY_IN_PROGRESS", Message: "a request with this Idempotency-Key is still being processed; retry later", Status: http.StatusConflict}
	ErrKeyReused  = &httpx.AppError{Code: "IDEMPOTENCY_KEY_REUSED", Message: "Idempotency-Key was already used for a different request", Status: http.StatusUnprocessableEntity}
	errBodyTooBig = &httpx.AppError{Code: "PAYLOAD_TOO_LARGE", Message: "request body too large", Status: http.StatusRequestEntityTooLarge}
	errBadBody    = &httpx.AppError{Code: "BAD_REQUEST", Message: "could not read request body", Status: http.StatusBadRequest}
)

// Store is the subset of db.Querier the middleware needs.
type Store interface {
	ReserveIdempotencyKey(ctx context.Context, arg db.ReserveIdempotencyKeyParams) (pgtype.UUID, error)
	GetIdempotencyKey(ctx context.Context, arg db.GetIdempotencyKeyParams) (db.GetIdempotencyKeyRow, error)
	CompleteIdempotencyKey(ctx context.Context, arg db.CompleteIdempotencyKeyParams) (int64, error)
	ReleaseIdempotencyKey(ctx context.Context, arg db.ReleaseIdempotencyKeyParams) error
	DeleteExpiredIdempotencyKeys(ctx context.Context) (int64, error)
}

// Guard holds the middleware configuration.
type Guard struct {
	Store Store
	TTL   time.Duration
	Lease time.Duration
}

// New returns a Guard with the default TTL and lease.
func New(store Store) *Guard {
	return &Guard{Store: store, TTL: DefaultTTL, Lease: DefaultLease}
}

// ValidKey reports whether k is 1-128 visible ASCII characters (0x21-0x7E).
func ValidKey(k string) bool {
	if len(k) == 0 || len(k) > maxKeyLen {
		return false
	}
	for i := 0; i < len(k); i++ {
		if k[i] < 0x21 || k[i] > 0x7e {
			return false
		}
	}
	return true
}

func isMutating(method string) bool {
	switch method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	}
	return false
}

// Wrap must run after authentication has put the user id on the request
// context (see auth.Middleware). Requests without the header, non-mutating
// requests and unauthenticated requests pass straight through.
func (g *Guard) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		values := r.Header.Values(HeaderKey)
		if len(values) == 0 || !isMutating(r.Method) {
			next.ServeHTTP(w, r)
			return
		}
		uidStr, ok := httpx.GetUserID(r.Context())
		if !ok {
			next.ServeHTTP(w, r)
			return
		}
		userID, err := uuid.Parse(uidStr)
		if err != nil {
			next.ServeHTTP(w, r)
			return
		}
		if len(values) != 1 || !ValidKey(values[0]) {
			httpx.WriteError(w, r, ErrInvalidKey)
			return
		}
		key := values[0]

		var body []byte
		if r.Body != nil {
			body, err = io.ReadAll(r.Body)
			_ = r.Body.Close()
			if err != nil {
				var tooBig *http.MaxBytesError
				if errors.As(err, &tooBig) {
					httpx.WriteError(w, r, errBodyTooBig)
				} else {
					httpx.WriteError(w, r, errBadBody)
				}
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(body))
		}
		sum := sha256.Sum256(body)
		fp := fingerprint{method: r.Method, path: r.URL.Path, hash: hex.EncodeToString(sum[:])}

		ctx := r.Context()
		// A conflicting row can vanish between the reserve and the read (its
		// request failed and released it), so retry the reservation a few times.
		for attempt := 0; attempt < 3; attempt++ {
			token := uuid.New()
			_, err := g.Store.ReserveIdempotencyKey(ctx, db.ReserveIdempotencyKeyParams{
				UserID:        userID,
				Key:           key,
				RequestMethod: fp.method,
				RequestPath:   fp.path,
				RequestHash:   fp.hash,
				LockToken:     pgtype.UUID{Bytes: token, Valid: true},
				TtlSeconds:    g.TTL.Seconds(),
				LeaseSeconds:  g.Lease.Seconds(),
			})
			if err == nil {
				g.run(w, r, next, userID, key, token)
				return
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				httpx.WriteError(w, r, err)
				return
			}
			existing, err := g.Store.GetIdempotencyKey(ctx, db.GetIdempotencyKeyParams{UserID: userID, Key: key})
			if errors.Is(err, pgx.ErrNoRows) {
				continue
			}
			if err != nil {
				httpx.WriteError(w, r, err)
				return
			}
			if existing.RequestMethod != fp.method || existing.RequestPath != fp.path || existing.RequestHash != fp.hash {
				httpx.WriteError(w, r, ErrKeyReused)
				return
			}
			if existing.State != "completed" {
				httpx.WriteError(w, r, ErrInProgress)
				return
			}
			replay(w, existing)
			return
		}
		httpx.WriteError(w, r, ErrInProgress)
	})
}

type fingerprint struct{ method, path, hash string }

func (g *Guard) run(w http.ResponseWriter, r *http.Request, next http.Handler, userID uuid.UUID, key string, token uuid.UUID) {
	lock := pgtype.UUID{Bytes: token, Valid: true}
	// Bookkeeping must survive a client disconnect cancelling the request context.
	bg := context.WithoutCancel(r.Context())
	keep := false
	defer func() {
		// Reached without keep on a 5xx or when the handler panics (the panic
		// keeps propagating to httpx.Recover); free the key so a retry runs again.
		if !keep {
			if err := g.Store.ReleaseIdempotencyKey(bg, db.ReleaseIdempotencyKeyParams{UserID: userID, Key: key, LockToken: lock}); err != nil {
				slog.Error("idempotency: release failed", "error", err, "path", r.URL.Path)
			}
		}
	}()

	rec := &recorder{ResponseWriter: w}
	next.ServeHTTP(rec, r)

	status := rec.status
	if status == 0 {
		status = http.StatusOK // net/http's implicit status when nothing was written
	}
	if status >= 500 {
		return
	}
	contentType := rec.Header().Get("Content-Type")
	if contentType == "" && rec.body.Len() > 0 {
		contentType = http.DetectContentType(rec.body.Bytes())
	}
	// Keep the reservation even if storing fails: a retry then sees 409 until
	// the lease lapses rather than immediately re-running a write that succeeded.
	keep = true
	if _, err := g.Store.CompleteIdempotencyKey(bg, db.CompleteIdempotencyKeyParams{
		UserID:              userID,
		Key:                 key,
		LockToken:           lock,
		ResponseStatus:      pgtype.Int4{Int32: int32(status), Valid: true},
		ResponseContentType: pgtype.Text{String: contentType, Valid: contentType != ""},
		ResponseBody:        rec.body.Bytes(),
	}); err != nil {
		slog.Error("idempotency: storing response failed", "error", err, "path", r.URL.Path)
	}
}

func replay(w http.ResponseWriter, row db.GetIdempotencyKeyRow) {
	h := w.Header()
	if row.ResponseContentType.Valid {
		h.Set("Content-Type", row.ResponseContentType.String)
	} else {
		h["Content-Type"] = nil // suppress sniffing; replay exactly what was sent
	}
	h.Set(HeaderReplayed, "true")
	status := http.StatusOK
	if row.ResponseStatus.Valid {
		status = int(row.ResponseStatus.Int32)
	}
	w.WriteHeader(status)
	_, _ = w.Write(row.ResponseBody)
}

// recorder passes the response through while keeping a copy of it.
type recorder struct {
	http.ResponseWriter
	status int
	body   bytes.Buffer
}

func (rw *recorder) WriteHeader(code int) {
	if rw.status == 0 {
		rw.status = code
	}
	rw.ResponseWriter.WriteHeader(code)
}

func (rw *recorder) Write(b []byte) (int, error) {
	if rw.status == 0 {
		rw.status = http.StatusOK
	}
	rw.body.Write(b)
	return rw.ResponseWriter.Write(b)
}

func (rw *recorder) Unwrap() http.ResponseWriter { return rw.ResponseWriter }

// Cleanup deletes expired keys. It is cheap and safe to call periodically.
func Cleanup(ctx context.Context, store Store) {
	n, err := store.DeleteExpiredIdempotencyKeys(ctx)
	if err != nil {
		slog.Error("idempotency: cleanup failed", "error", err)
		return
	}
	if n > 0 {
		slog.Info("idempotency: expired keys removed", "count", n)
	}
}
