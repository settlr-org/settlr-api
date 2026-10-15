// Package wallet implements the personal-finance wallet (docs/design/wallet.md):
// accounts, transactions, tags, the unified ledger (wallet transactions plus group
// expense shares and settlements), overview, settings and exchange-rate overrides
// (phase 1), and planned payments, budgets and loans (phase 2).
package wallet

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/settlr-org/settlr-api/internal/auth"
	db "github.com/settlr-org/settlr-api/internal/db"
	"github.com/settlr-org/settlr-api/internal/httpx"
	"github.com/settlr-org/settlr-api/internal/rates"
)

// Handler serves the wallet endpoints. Rates converts between currencies for
// balances and totals; when nil only identity conversions and user overrides work.
type Handler struct {
	Pool    *pgxpool.Pool
	Queries *db.Queries
	Rates   *rates.Converter
}

func (h *Handler) q() *db.Queries {
	if h.Queries == nil && h.Pool != nil {
		h.Queries = db.New(h.Pool)
	}
	return h.Queries
}

func (h *Handler) RegisterRoutes(mux *http.ServeMux, authMw func(http.Handler) http.Handler) {
	mux.Handle("GET /api/v1/accounts", authMw(http.HandlerFunc(h.ListAccounts)))
	mux.Handle("POST /api/v1/accounts", authMw(http.HandlerFunc(h.CreateAccount)))
	mux.Handle("POST /api/v1/accounts/reorder", authMw(http.HandlerFunc(h.ReorderAccounts)))
	mux.Handle("GET /api/v1/accounts/{id}", authMw(http.HandlerFunc(h.GetAccount)))
	mux.Handle("PATCH /api/v1/accounts/{id}", authMw(http.HandlerFunc(h.UpdateAccount)))
	mux.Handle("DELETE /api/v1/accounts/{id}", authMw(http.HandlerFunc(h.DeleteAccount)))
	mux.Handle("POST /api/v1/accounts/{id}/adjust", authMw(http.HandlerFunc(h.AdjustAccount)))
	mux.Handle("GET /api/v1/accounts/{id}/summary", authMw(http.HandlerFunc(h.AccountSummary)))

	mux.Handle("GET /api/v1/transactions", authMw(http.HandlerFunc(h.ListTransactions)))
	mux.Handle("POST /api/v1/transactions", authMw(http.HandlerFunc(h.CreateTransaction)))
	mux.Handle("GET /api/v1/transactions/{id}", authMw(http.HandlerFunc(h.GetTransaction)))
	mux.Handle("PATCH /api/v1/transactions/{id}", authMw(http.HandlerFunc(h.UpdateTransaction)))
	mux.Handle("DELETE /api/v1/transactions/{id}", authMw(http.HandlerFunc(h.DeleteTransaction)))
	mux.Handle("POST /api/v1/transactions/{id}/duplicate", authMw(http.HandlerFunc(h.DuplicateTransaction)))
	mux.Handle("POST /api/v1/transactions/{id}/pay", authMw(http.HandlerFunc(h.PayTransaction)))
	mux.Handle("POST /api/v1/transactions/{id}/skip", authMw(http.HandlerFunc(h.SkipTransaction)))
	mux.Handle("POST /api/v1/transactions/skip-overdue", authMw(http.HandlerFunc(h.SkipOverdue)))

	mux.Handle("GET /api/v1/planned", authMw(http.HandlerFunc(h.ListPlanned)))
	mux.Handle("POST /api/v1/planned", authMw(http.HandlerFunc(h.CreatePlanned)))
	mux.Handle("GET /api/v1/planned/{id}", authMw(http.HandlerFunc(h.GetPlanned)))
	mux.Handle("PATCH /api/v1/planned/{id}", authMw(http.HandlerFunc(h.UpdatePlanned)))
	mux.Handle("DELETE /api/v1/planned/{id}", authMw(http.HandlerFunc(h.DeletePlanned)))

	mux.Handle("GET /api/v1/budgets", authMw(http.HandlerFunc(h.ListBudgets)))
	mux.Handle("POST /api/v1/budgets", authMw(http.HandlerFunc(h.CreateBudget)))
	mux.Handle("POST /api/v1/budgets/reorder", authMw(http.HandlerFunc(h.ReorderBudgets)))
	mux.Handle("GET /api/v1/budgets/{id}", authMw(http.HandlerFunc(h.GetBudget)))
	mux.Handle("PATCH /api/v1/budgets/{id}", authMw(http.HandlerFunc(h.UpdateBudget)))
	mux.Handle("DELETE /api/v1/budgets/{id}", authMw(http.HandlerFunc(h.DeleteBudget)))

	mux.Handle("GET /api/v1/loans", authMw(http.HandlerFunc(h.ListLoans)))
	mux.Handle("POST /api/v1/loans", authMw(http.HandlerFunc(h.CreateLoan)))
	mux.Handle("POST /api/v1/loans/reorder", authMw(http.HandlerFunc(h.ReorderLoans)))
	mux.Handle("GET /api/v1/loans/{id}", authMw(http.HandlerFunc(h.GetLoan)))
	mux.Handle("PATCH /api/v1/loans/{id}", authMw(http.HandlerFunc(h.UpdateLoan)))
	mux.Handle("DELETE /api/v1/loans/{id}", authMw(http.HandlerFunc(h.DeleteLoan)))
	mux.Handle("GET /api/v1/loans/{id}/records", authMw(http.HandlerFunc(h.ListLoanRecords)))
	mux.Handle("POST /api/v1/loans/{id}/records", authMw(http.HandlerFunc(h.CreateLoanRecord)))
	mux.Handle("PATCH /api/v1/loan-records/{id}", authMw(http.HandlerFunc(h.UpdateLoanRecord)))
	mux.Handle("DELETE /api/v1/loan-records/{id}", authMw(http.HandlerFunc(h.DeleteLoanRecord)))

	mux.Handle("GET /api/v1/tags", authMw(http.HandlerFunc(h.ListTags)))
	mux.Handle("POST /api/v1/tags", authMw(http.HandlerFunc(h.CreateTag)))
	mux.Handle("GET /api/v1/tags/{id}", authMw(http.HandlerFunc(h.GetTag)))
	mux.Handle("PATCH /api/v1/tags/{id}", authMw(http.HandlerFunc(h.UpdateTag)))
	mux.Handle("DELETE /api/v1/tags/{id}", authMw(http.HandlerFunc(h.DeleteTag)))

	mux.Handle("GET /api/v1/ledger", authMw(http.HandlerFunc(h.Ledger)))
	mux.Handle("GET /api/v1/overview", authMw(http.HandlerFunc(h.Overview)))
	mux.Handle("GET /api/v1/categories/{id}/summary", authMw(http.HandlerFunc(h.CategorySummary)))

	mux.Handle("GET /api/v1/me/settings", authMw(http.HandlerFunc(h.GetSettings)))
	mux.Handle("PATCH /api/v1/me/settings", authMw(http.HandlerFunc(h.UpdateSettings)))

	mux.Handle("GET /api/v1/me/rates", authMw(http.HandlerFunc(h.ListRateOverrides)))
	mux.Handle("PUT /api/v1/me/rates/{base}/{quote}", authMw(http.HandlerFunc(h.PutRateOverride)))
	mux.Handle("DELETE /api/v1/me/rates/{base}/{quote}", authMw(http.HandlerFunc(h.DeleteRateOverride)))
}

// ---------------------------------------------------------------- request helpers

func currentUser(r *http.Request) uuid.UUID {
	uid, _ := httpx.GetUserID(r.Context())
	id, _ := uuid.Parse(uid)
	return id
}

func writeErr(w http.ResponseWriter, status int, code, msg string) {
	httpx.WriteJSON(w, status, map[string]any{"error": map[string]string{"code": code, "message": msg}})
}

func validation(w http.ResponseWriter, msg string) {
	writeErr(w, http.StatusUnprocessableEntity, "VALIDATION_ERROR", msg)
}

// decode reads a JSON body; on failure it writes 400 and returns false.
func decode(w http.ResponseWriter, r *http.Request, dst any) bool {
	if err := httpx.DecodeJSON(r, dst); err != nil {
		writeErr(w, http.StatusBadRequest, "VALIDATION_ERROR", err.Error())
		return false
	}
	return true
}

func pathID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		validation(w, "invalid id")
		return uuid.Nil, false
	}
	return id, true
}

// Opt distinguishes an absent JSON field (Set=false) from an explicit null (Null=true).
type Opt[T any] struct {
	Set  bool
	Null bool
	V    T
}

func (o *Opt[T]) UnmarshalJSON(b []byte) error {
	o.Set = true
	if string(b) == "null" {
		o.Null = true
		return nil
	}
	return json.Unmarshal(b, &o.V)
}

// Value reports the value when present and not null.
func (o Opt[T]) Value() (T, bool) {
	return o.V, o.Set && !o.Null
}

func normCurrency(s string) string { return strings.ToUpper(strings.TrimSpace(s)) }

func validCurrency(s string) bool { return auth.SupportedCurrencies[s] }

// parseTime accepts RFC 3339 timestamps, "YYYY-MM-DDTHH:MM:SS" (UTC) and plain dates (UTC midnight).
func parseTime(s string) (time.Time, bool, error) {
	s = strings.TrimSpace(s)
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t, false, nil
	}
	if t, err := time.Parse("2006-01-02T15:04:05", s); err == nil {
		return t.UTC(), false, nil
	}
	if t, err := time.Parse("2006-01-02", s); err == nil {
		return t.UTC(), true, nil
	}
	return time.Time{}, false, fmt.Errorf("invalid time %q (use RFC 3339 or YYYY-MM-DD)", s)
}

// parseRange reads ?from&to. A date-only "to" is inclusive (the whole day).
func parseRange(r *http.Request) (from, to *time.Time, err error) {
	if s := r.URL.Query().Get("from"); s != "" {
		t, _, perr := parseTime(s)
		if perr != nil {
			return nil, nil, perr
		}
		from = &t
	}
	if s := r.URL.Query().Get("to"); s != "" {
		t, dateOnly, perr := parseTime(s)
		if perr != nil {
			return nil, nil, perr
		}
		if dateOnly {
			t = t.AddDate(0, 0, 1)
		}
		to = &t
	}
	return from, to, nil
}

func ts(t *time.Time) pgtype.Timestamptz {
	if t == nil {
		return pgtype.Timestamptz{}
	}
	return pgtype.Timestamptz{Time: *t, Valid: true}
}

func tsPtr(t pgtype.Timestamptz) *time.Time {
	if !t.Valid {
		return nil
	}
	v := t.Time.UTC()
	return &v
}

func pgUUID(id *uuid.UUID) pgtype.UUID {
	if id == nil || *id == uuid.Nil {
		return pgtype.UUID{}
	}
	return pgtype.UUID{Bytes: *id, Valid: true}
}

func uuidPtr(u pgtype.UUID) *uuid.UUID {
	if !u.Valid {
		return nil
	}
	id := uuid.UUID(u.Bytes)
	return &id
}

func nilIfZero(id uuid.UUID) *uuid.UUID {
	if id == uuid.Nil {
		return nil
	}
	return &id
}

func int8Ptr(v pgtype.Int8) *int64 {
	if !v.Valid {
		return nil
	}
	x := v.Int64
	return &x
}

func pgInt8(v *int64) pgtype.Int8 {
	if v == nil {
		return pgtype.Int8{}
	}
	return pgtype.Int8{Int64: *v, Valid: true}
}

func pgUUIDs(ids []uuid.UUID) []pgtype.UUID {
	out := make([]pgtype.UUID, 0, len(ids))
	for _, id := range ids {
		out = append(out, pgtype.UUID{Bytes: id, Valid: true})
	}
	return out
}

func uniqueUUIDs(ids []uuid.UUID) []uuid.UUID {
	seen := make(map[uuid.UUID]bool, len(ids))
	out := make([]uuid.UUID, 0, len(ids))
	for _, id := range ids {
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}

// queryUUIDs parses a comma-separated (or repeated) uuid list query parameter.
func queryUUIDs(r *http.Request, name string) ([]uuid.UUID, error) {
	return parseUUIDs(r.URL.Query()[name], name)
}

func parseUUIDs(values []string, name string) ([]uuid.UUID, error) {
	out := []uuid.UUID{}
	for _, raw := range values {
		for _, s := range strings.Split(raw, ",") {
			s = strings.TrimSpace(s)
			if s == "" {
				continue
			}
			id, err := uuid.Parse(s)
			if err != nil {
				return nil, fmt.Errorf("invalid %s", name)
			}
			out = append(out, id)
		}
	}
	return out, nil
}

func queryList(r *http.Request, name string) []string {
	out := []string{}
	for _, raw := range r.URL.Query()[name] {
		for _, s := range strings.Split(raw, ",") {
			if s = strings.ToUpper(strings.TrimSpace(s)); s != "" {
				out = append(out, s)
			}
		}
	}
	return out
}

func queryInt64(r *http.Request, name string) (pgtype.Int8, error) {
	s := strings.TrimSpace(r.URL.Query().Get(name))
	if s == "" {
		return pgtype.Int8{}, nil
	}
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return pgtype.Int8{}, fmt.Errorf("%s must be an integer (minor units)", name)
	}
	return pgtype.Int8{Int64: v, Valid: true}, nil
}

func queryLimit(r *http.Request, def, max int) int {
	if v, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && v > 0 {
		if v > max {
			return max
		}
		return v
	}
	return def
}

// plannedMode maps ?planned=true|only / all / (default) false to the SQL switch.
func plannedMode(r *http.Request) string {
	switch strings.ToLower(strings.TrimSpace(r.URL.Query().Get("planned"))) {
	case "true", "1", "only":
		return "only"
	case "all":
		return "all"
	default:
		return "exclude"
	}
}

// Cursors are opaque to clients: base64url("<unix micros>|<id>") of the last row.
func encodeCursor(t time.Time, id uuid.UUID) string {
	return base64.RawURLEncoding.EncodeToString([]byte(strconv.FormatInt(t.UnixMicro(), 10) + "|" + id.String()))
}

func decodeCursor(s string) (pgtype.Timestamptz, pgtype.UUID, error) {
	if s == "" {
		return pgtype.Timestamptz{}, pgtype.UUID{}, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return pgtype.Timestamptz{}, pgtype.UUID{}, errors.New("invalid cursor")
	}
	parts := strings.SplitN(string(raw), "|", 2)
	if len(parts) != 2 {
		return pgtype.Timestamptz{}, pgtype.UUID{}, errors.New("invalid cursor")
	}
	us, err1 := strconv.ParseInt(parts[0], 10, 64)
	id, err2 := uuid.Parse(parts[1])
	if err1 != nil || err2 != nil {
		return pgtype.Timestamptz{}, pgtype.UUID{}, errors.New("invalid cursor")
	}
	return pgtype.Timestamptz{Time: time.UnixMicro(us).UTC(), Valid: true}, pgtype.UUID{Bytes: id, Valid: true}, nil
}

func isNoRows(err error) bool { return errors.Is(err, pgx.ErrNoRows) }

// ---------------------------------------------------------------- currency conversion

// fx converts amounts for one request: the user's overrides, then the shared converter.
// Currencies that could not be converted are collected in missing.
type fx struct {
	h       *Handler
	ov      rates.Overrides
	base    string
	missing map[string]bool
}

func (h *Handler) newFX(ctx context.Context, userID uuid.UUID) (*fx, error) {
	base, err := h.q().GetUserDefaultCurrency(ctx, userID)
	if err != nil {
		return nil, err
	}
	rows, err := h.q().ListRateOverrides(ctx, userID)
	if err != nil {
		return nil, err
	}
	ov := rates.Overrides{}
	for _, row := range rows {
		ov[rates.OverrideKey(strings.TrimSpace(row.Base), strings.TrimSpace(row.Quote))] = row.Rate
	}
	return &fx{h: h, ov: ov, base: strings.TrimSpace(base), missing: map[string]bool{}}, nil
}

func (f *fx) convert(ctx context.Context, amount int64, from, to string) (int64, bool) {
	from, to = strings.TrimSpace(from), strings.TrimSpace(to)
	if from == to || amount == 0 {
		return amount, true
	}
	v, ok := f.h.Rates.Convert(ctx, f.ov, amount, from, to)
	if !ok {
		f.missing[from+"/"+to] = true
	}
	return v, ok
}

func (f *fx) missingList() []string {
	out := make([]string, 0, len(f.missing))
	for k := range f.missing {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// balances returns each live account's balance in its own currency, plus, per
// account, the currency pairs that could not be converted (the balance is then
// incomplete).
func (h *Handler) balances(ctx context.Context, userID uuid.UUID, accounts []db.Account, f *fx) (map[uuid.UUID]int64, map[uuid.UUID][]string, error) {
	rows, err := h.q().AccountBalances(ctx, db.AccountBalancesParams{UserID: userID})
	if err != nil {
		return nil, nil, err
	}
	cur := make(map[uuid.UUID]string, len(accounts))
	out := make(map[uuid.UUID]int64, len(accounts))
	missing := make(map[uuid.UUID][]string)
	for _, a := range accounts {
		cur[a.ID] = strings.TrimSpace(a.Currency)
		out[a.ID] = 0
	}
	for _, row := range rows {
		if !row.AccountID.Valid {
			continue
		}
		id := uuid.UUID(row.AccountID.Bytes)
		c, ok := cur[id]
		if !ok {
			continue
		}
		if v, ok := f.convert(ctx, row.Total, row.Currency, c); ok {
			out[id] += v
		} else {
			missing[id] = append(missing[id], strings.TrimSpace(row.Currency)+"/"+c)
		}
	}
	return out, missing, nil
}

func pu(id uuid.UUID) pgtype.UUID { return pgtype.UUID{Bytes: id, Valid: true} }

// nonNil turns a nil slice into an empty one (sqlc binds nil as SQL NULL, which
// breaks cardinality() checks).
func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
