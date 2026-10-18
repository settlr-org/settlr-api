// Package syncstore stores the end-to-end-encrypted copy of a user's on-device wallet. Everything it holds is
// ciphertext produced on the user's devices: the key that decrypts it is wrapped with the user's recovery
// passphrase on the client and is never available here. See docs/design/local-first.md.
package syncstore

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	db "github.com/settlr-org/settlr-api/internal/db"
	"github.com/settlr-org/settlr-api/internal/httpx"
)

// Limits keep one account from using unbounded storage.
const (
	maxRecordBytes  = 64 << 10  // one encrypted record
	maxBatch        = 200       // records per POST
	maxRecords      = 500_000   // per user
	maxTotalBytes   = 256 << 20 // per user
	maxKeyBytes     = 4 << 10
	defaultPageSize = 500
	maxPageSize     = 1000
)

type Handler struct {
	Pool    *pgxpool.Pool
	Queries *db.Queries
}

func (h *Handler) q() *db.Queries {
	if h.Queries == nil && h.Pool != nil {
		h.Queries = db.New(h.Pool)
	}
	return h.Queries
}

func (h *Handler) RegisterRoutes(mux *http.ServeMux, authMw func(http.Handler) http.Handler) {
	mux.Handle("GET /api/v1/sync/status", authMw(http.HandlerFunc(h.Status)))
	mux.Handle("GET /api/v1/sync/key", authMw(http.HandlerFunc(h.GetKey)))
	mux.Handle("PUT /api/v1/sync/key", authMw(http.HandlerFunc(h.PutKey)))
	mux.Handle("GET /api/v1/sync/changes", authMw(http.HandlerFunc(h.Changes)))
	mux.Handle("POST /api/v1/sync/records", authMw(http.HandlerFunc(h.PushRecords)))
	mux.Handle("DELETE /api/v1/sync", authMw(http.HandlerFunc(h.Disable)))
}

func currentUser(r *http.Request) uuid.UUID {
	uid, _ := httpx.GetUserID(r.Context())
	id, _ := uuid.Parse(uid)
	return id
}

func validation(w http.ResponseWriter, msg string) {
	httpx.WriteJSON(w, http.StatusUnprocessableEntity, map[string]any{"error": map[string]string{"code": "VALIDATION_ERROR", "message": msg}})
}

func b64(b []byte) string { return base64.StdEncoding.EncodeToString(b) }

func unb64(s string) ([]byte, bool) {
	b, err := base64.StdEncoding.DecodeString(s)
	return b, err == nil
}

// Status reports whether a key exists and how much is stored.
func (h *Handler) Status(w http.ResponseWriter, r *http.Request) {
	uid := currentUser(r)
	st, err := h.q().SyncStatus(r.Context(), uid)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	_, kerr := h.q().GetSyncKey(r.Context(), uid)
	if kerr != nil && kerr != pgx.ErrNoRows {
		httpx.WriteError(w, r, kerr)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"has_key": kerr == nil, "records": st.Records, "bytes": st.Bytes, "latest_seq": st.LatestSeq})
}

func (h *Handler) GetKey(w http.ResponseWriter, r *http.Request) {
	k, err := h.q().GetSyncKey(r.Context(), currentUser(r))
	if err == pgx.ErrNoRows {
		httpx.WriteError(w, r, httpx.ErrNotFound)
		return
	}
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"kdf": k.Kdf, "kdf_params": json.RawMessage(k.KdfParams), "salt": b64(k.Salt), "wrapped_key": b64(k.WrappedKey), "version": k.Version,
	})
}

// PutKey stores the passphrase-wrapped data key. expected_version 0 creates it (409 when one exists); a positive
// value replaces it only if that is still the current version (a passphrase change).
func (h *Handler) PutKey(w http.ResponseWriter, r *http.Request) {
	var req struct {
		KDF             string          `json:"kdf"`
		KDFParams       json.RawMessage `json:"kdf_params"`
		Salt            string          `json:"salt"`
		WrappedKey      string          `json:"wrapped_key"`
		ExpectedVersion int32           `json:"expected_version"`
	}
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteJSON(w, http.StatusBadRequest, map[string]any{"error": map[string]string{"code": "VALIDATION_ERROR", "message": err.Error()}})
		return
	}
	salt, ok1 := unb64(req.Salt)
	wrapped, ok2 := unb64(req.WrappedKey)
	var params map[string]any
	if req.KDF == "" || len(req.KDF) > 40 || !ok1 || !ok2 || len(salt) < 16 || len(salt) > 64 || len(wrapped) < 32 || len(wrapped) > maxKeyBytes ||
		json.Unmarshal(req.KDFParams, &params) != nil || len(req.KDFParams) > 1024 {
		validation(w, "kdf, kdf_params, salt (16-64 bytes) and wrapped_key (base64) are required")
		return
	}
	uid := currentUser(r)
	var n int64
	var err error
	if req.ExpectedVersion <= 0 {
		n, err = h.q().InsertSyncKey(r.Context(), db.InsertSyncKeyParams{UserID: uid, Kdf: req.KDF, KdfParams: req.KDFParams, Salt: salt, WrappedKey: wrapped})
	} else {
		n, err = h.q().UpdateSyncKey(r.Context(), db.UpdateSyncKeyParams{UserID: uid, Kdf: req.KDF, KdfParams: req.KDFParams, Salt: salt, WrappedKey: wrapped, Version: req.ExpectedVersion})
	}
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if n == 0 {
		httpx.WriteJSON(w, http.StatusConflict, map[string]any{"error": map[string]string{"code": "CONFLICT", "message": "the sync key changed (or already exists); fetch it and retry"}})
		return
	}
	k, err := h.q().GetSyncKey(r.Context(), uid)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"version": k.Version})
}

type recordOut struct {
	ID         uuid.UUID `json:"id"`
	Seq        int64     `json:"seq"`
	Deleted    bool      `json:"deleted"`
	Nonce      string    `json:"nonce"`
	Ciphertext string    `json:"ciphertext"`
}

// Changes returns the records after ?since (a sequence number, default 0), oldest first.
func (h *Handler) Changes(w http.ResponseWriter, r *http.Request) {
	since := int64(0)
	if s := r.URL.Query().Get("since"); s != "" {
		v, err := strconv.ParseInt(s, 10, 64)
		if err != nil || v < 0 {
			validation(w, "since must be a non-negative integer")
			return
		}
		since = v
	}
	limit := defaultPageSize
	if s := r.URL.Query().Get("limit"); s != "" {
		if v, err := strconv.Atoi(s); err == nil && v > 0 {
			limit = min(v, maxPageSize)
		}
	}
	rows, err := h.q().ListSyncChanges(r.Context(), db.ListSyncChangesParams{UserID: currentUser(r), Seq: since, Limit: int32(limit + 1)})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	more := len(rows) > limit
	if more {
		rows = rows[:limit]
	}
	out := make([]recordOut, 0, len(rows))
	last := since
	for _, row := range rows {
		out = append(out, recordOut{ID: row.ID, Seq: row.Seq, Deleted: row.Deleted, Nonce: b64(row.Nonce), Ciphertext: b64(row.Ciphertext)})
		last = row.Seq
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"records": out, "next_seq": last, "has_more": more})
}

type recordIn struct {
	ID         uuid.UUID `json:"id"`
	Deleted    bool      `json:"deleted"`
	Nonce      string    `json:"nonce"`
	Ciphertext string    `json:"ciphertext"`
}

// PushRecords stores a batch of encrypted records (upserts by id) and numbers them in order.
func (h *Handler) PushRecords(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Records []recordIn `json:"records"`
	}
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteJSON(w, http.StatusBadRequest, map[string]any{"error": map[string]string{"code": "VALIDATION_ERROR", "message": err.Error()}})
		return
	}
	if len(req.Records) == 0 || len(req.Records) > maxBatch {
		validation(w, "records must hold 1-"+strconv.Itoa(maxBatch)+" items")
		return
	}
	type decoded struct {
		in        recordIn
		nonce, ct []byte
	}
	recs := make([]decoded, 0, len(req.Records))
	for _, in := range req.Records {
		nonce, ok1 := unb64(in.Nonce)
		ct, ok2 := unb64(in.Ciphertext)
		if in.ID == uuid.Nil || !ok1 || !ok2 || len(nonce) != 12 || len(ct) < 16 || len(ct) > maxRecordBytes {
			validation(w, "each record needs an id, a 12-byte nonce and a ciphertext of 16 bytes to 64 KiB (base64)")
			return
		}
		recs = append(recs, decoded{in, nonce, ct})
	}
	ctx := r.Context()
	uid := currentUser(r)
	st, err := h.q().SyncStatus(ctx, uid)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if st.Records+int64(len(recs)) > maxRecords || st.Bytes > maxTotalBytes {
		httpx.WriteJSON(w, http.StatusRequestEntityTooLarge, map[string]any{"error": map[string]string{"code": "SYNC_QUOTA", "message": "the encrypted copy has reached its size limit"}})
		return
	}
	if _, err := h.q().GetSyncKey(ctx, uid); err == pgx.ErrNoRows {
		validation(w, "create the sync key first (PUT /sync/key)")
		return
	} else if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	tx, err := h.Pool.Begin(ctx)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := h.q().WithTx(tx)
	if err := qtx.EnsureSyncCounter(ctx, uid); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	first, err := qtx.ReserveSyncSeqs(ctx, db.ReserveSyncSeqsParams{N: int64(len(recs)), UserID: uid})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	for i, rec := range recs {
		if err := qtx.UpsertSyncRecord(ctx, db.UpsertSyncRecordParams{UserID: uid, ID: rec.in.ID, Seq: first + int64(i), Deleted: rec.in.Deleted, Nonce: rec.nonce, Ciphertext: rec.ct}); err != nil {
			httpx.WriteError(w, r, err)
			return
		}
	}
	if err := tx.Commit(ctx); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"accepted": len(recs), "latest_seq": first + int64(len(recs)) - 1})
}

// Disable erases the whole encrypted copy: the key and every record.
func (h *Handler) Disable(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("confirm") != "DELETE" {
		validation(w, "confirm=DELETE is required to delete the encrypted copy")
		return
	}
	if err := EraseUser(r.Context(), h.Pool, currentUser(r)); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"message": "sync data deleted"})
}

// EraseUser deletes a user's key and encrypted records in one transaction (also used when the account is deleted).
func EraseUser(ctx context.Context, pool *pgxpool.Pool, userID uuid.UUID) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := db.New(pool).WithTx(tx)
	if err := q.DeleteSyncRecords(ctx, userID); err != nil {
		return err
	}
	if err := q.DeleteSyncCounter(ctx, userID); err != nil {
		return err
	}
	if err := q.DeleteSyncKey(ctx, userID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
