package export

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	db "github.com/settlr-org/settlr-api/internal/db"
	"github.com/settlr-org/settlr-api/internal/httpx"
)

// BackupVersion is the "version" of the document served by GET /me/export.json.
// Version 2 added the wallet sections; the version 1 shape was {"data":[...]}.
const BackupVersion = 2

// pageSize is the number of transactions fetched per round trip while streaming.
const pageSize = 1000

func tsOrNil(t pgtype.Timestamptz) *time.Time {
	if !t.Valid {
		return nil
	}
	v := t.Time.UTC()
	return &v
}

func tsReq(t pgtype.Timestamptz) time.Time { return t.Time.UTC() }

func uuidOrNil(u pgtype.UUID) *uuid.UUID {
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

func int8OrNil(v pgtype.Int8) *int64 {
	if !v.Valid {
		return nil
	}
	x := v.Int64
	return &x
}

func textOrNil(v pgtype.Text) *string {
	if !v.Valid {
		return nil
	}
	return &v.String
}

func uuidList(in []pgtype.UUID) []uuid.UUID {
	out := make([]uuid.UUID, 0, len(in))
	for _, u := range in {
		if u.Valid {
			out = append(out, uuid.UUID(u.Bytes))
		}
	}
	return out
}

func pgUUID(id uuid.UUID) pgtype.UUID { return pgtype.UUID{Bytes: id, Valid: true} }

func cur(s string) string { return strings.TrimSpace(s) }

// ---------------------------------------------------------------- JSON backup

// jsonDoc writes a JSON object key by key so large arrays can be streamed.
type jsonDoc struct {
	w     io.Writer
	flush func()
	n     int
}

func (d *jsonDoc) write(s string) { _, _ = io.WriteString(d.w, s) }

func (d *jsonDoc) key(k string) {
	if d.n > 0 {
		d.write(",")
	}
	d.n++
	b, _ := json.Marshal(k)
	d.write(string(b))
	d.write(":")
}

func (d *jsonDoc) value(k string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	d.key(k)
	d.write(string(b))
	return nil
}

// stream writes k as an array; next returns the following chunk of elements (empty = done).
func (d *jsonDoc) stream(k string, first []any, next func() ([]any, error)) error {
	d.key(k)
	d.write("[")
	n := 0
	chunk := first
	for len(chunk) > 0 {
		for _, el := range chunk {
			b, err := json.Marshal(el)
			if err != nil {
				return err
			}
			if n > 0 {
				d.write(",")
			}
			n++
			d.write(string(b))
		}
		d.flush()
		var err error
		if chunk, err = next(); err != nil {
			return err
		}
	}
	d.write("]")
	return nil
}

func transactionDoc(t db.ExportTransactionsPageRow) map[string]any {
	return map[string]any{
		"id": t.ID, "type": t.Type, "account_id": uuidOrNil(t.AccountID), "to_account_id": uuidOrNil(t.ToAccountID),
		"amount": t.Amount, "currency": cur(t.Currency), "to_amount": int8OrNil(t.ToAmount),
		"category_id": nilIfZero(t.CategoryID), "title": t.Title, "description": t.Description,
		"occurred_at": tsOrNil(t.OccurredAt), "due_at": tsOrNil(t.DueAt), "planned": !t.OccurredAt.Valid,
		"planned_payment_id": uuidOrNil(t.PlannedPaymentID), "loan_id": uuidOrNil(t.LoanID), "loan_record_id": uuidOrNil(t.LoanRecordID),
		"source": t.Source, "import_ref": textOrNil(t.ImportRef), "tag_ids": uuidList(t.TagIds),
		"created_at": tsReq(t.CreatedAt), "updated_at": tsReq(t.UpdatedAt),
	}
}

// MyJSON serves the account backup document (see docs/design/wallet.md, "Backup format").
// Sections are loaded before the first byte is written so a failure still yields a
// proper error response; only the transaction pages are fetched while streaming.
func (h *Handler) MyJSON(w http.ResponseWriter, r *http.Request) {
	uid, _ := httpx.GetUserID(r.Context())
	userID, _ := uuid.Parse(uid)
	q := h.ensureQueries()
	if q == nil {
		httpx.WriteError(w, r, fmt.Errorf("no db queries available"))
		return
	}
	ctx := r.Context()
	doc, err := loadBackup(ctx, q, userID)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	// First page up front: an error here is still a clean 500.
	page, err := q.ExportTransactionsPage(ctx, db.ExportTransactionsPageParams{UserID: userID, AfterID: pgUUID(uuid.Nil), Lim: pageSize})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}

	longWrite(w)
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Disposition", "attachment; filename=\"settlr-all-data.json\"")
	rc := http.NewResponseController(w)
	d := &jsonDoc{w: w, flush: func() { _ = rc.Flush() }}
	d.write("{")
	fail := func(err error) {
		// Headers are gone; abort the connection so clients see a failed download
		// instead of a silently truncated backup.
		slog.Error("export: backup aborted", "error", err, "user_id", userID)
		panic(http.ErrAbortHandler)
	}
	sections := []struct {
		k string
		v any
	}{
		{"version", BackupVersion}, {"exported_at", time.Now().UTC()},
		{"user", doc.user}, {"settings", doc.settings}, {"rate_overrides", doc.rates},
		{"accounts", doc.accounts}, {"categories", doc.categories}, {"category_order", doc.categoryOrder}, {"tags", doc.tags},
	}
	for _, s := range sections {
		if err := d.value(s.k, s.v); err != nil {
			fail(err)
		}
	}
	after := uuid.Nil
	toEls := func(rows []db.ExportTransactionsPageRow) []any {
		els := make([]any, len(rows))
		for i, t := range rows {
			els[i] = transactionDoc(t)
		}
		if len(rows) > 0 {
			after = rows[len(rows)-1].ID
		}
		return els
	}
	if err := d.stream("transactions", toEls(page), func() ([]any, error) {
		if len(page) < pageSize {
			return nil, nil
		}
		var err error
		page, err = q.ExportTransactionsPage(ctx, db.ExportTransactionsPageParams{UserID: userID, AfterID: pgUUID(after), Lim: pageSize})
		return toEls(page), err
	}); err != nil {
		fail(err)
	}
	for _, s := range []struct {
		k string
		v any
	}{
		{"planned_payments", doc.planned}, {"budgets", doc.budgets}, {"loans", doc.loans},
		{"groups", doc.groups}, {"data", doc.legacyData},
	} {
		if err := d.value(s.k, s.v); err != nil {
			fail(err)
		}
	}
	d.write("}\n")
}

type backup struct {
	user, settings                                   map[string]any
	rates, accounts, categories, categoryOrder, tags []map[string]any
	planned, budgets, loans, groups, legacyData      []map[string]any
}

func loadBackup(ctx context.Context, q *db.Queries, userID uuid.UUID) (*backup, error) {
	b := &backup{}
	p, err := q.ExportUserProfile(ctx, userID)
	if err != nil {
		return nil, err
	}
	// No password hash, tokens, sessions or provider ids are ever part of the export.
	b.user = map[string]any{
		"id": p.ID, "name": p.Name, "email": p.Email, "avatar_url": textOrNil(p.AvatarUrl),
		"default_currency": cur(p.DefaultCurrency), "timezone": p.Timezone, "email_verified_at": tsOrNil(p.EmailVerifiedAt),
		"bank_name": textOrNil(p.BankName), "payment_handle": textOrNil(p.PaymentHandle), "bank_qr_url": textOrNil(p.BankQrUrl),
		"created_at": tsReq(p.CreatedAt), "updated_at": tsReq(p.UpdatedAt),
	}

	s, err := q.GetUserSettings(ctx, userID)
	if errors.Is(err, pgx.ErrNoRows) {
		s, err = db.UserSetting{UserID: userID, MonthStartDay: 1, Theme: "auto", ReminderTime: "20:00"}, nil
	}
	if err != nil {
		return nil, err
	}
	b.settings = map[string]any{
		"month_start_day": s.MonthStartDay, "hide_balance": s.HideBalance, "hide_income": s.HideIncome,
		"treat_transfers_as_flow": s.TreatTransfersAsFlow, "theme": s.Theme, "reminder_enabled": s.ReminderEnabled,
		"reminder_time": s.ReminderTime, "onboarded_at": tsOrNil(s.OnboardedAt),
	}

	rates, err := q.ListRateOverrides(ctx, userID)
	if err != nil {
		return nil, err
	}
	b.rates = []map[string]any{}
	for _, r := range rates {
		b.rates = append(b.rates, map[string]any{"base": cur(r.Base), "quote": cur(r.Quote), "rate": r.Rate, "updated_at": tsReq(r.UpdatedAt)})
	}

	accs, err := q.ExportAccounts(ctx, userID)
	if err != nil {
		return nil, err
	}
	b.accounts = []map[string]any{}
	for _, a := range accs {
		b.accounts = append(b.accounts, map[string]any{
			"id": a.ID, "name": a.Name, "currency": cur(a.Currency), "color": a.Color, "icon": a.Icon,
			"include_in_balance": a.IncludeInBalance, "is_default": a.IsDefault, "sort_order": a.SortOrder,
			"archived_at": tsOrNil(a.ArchivedAt), "created_at": tsReq(a.CreatedAt), "updated_at": tsReq(a.UpdatedAt),
		})
	}

	cats, err := q.ExportOwnedCategories(ctx, userID)
	if err != nil {
		return nil, err
	}
	b.categories = []map[string]any{}
	for _, c := range cats {
		b.categories = append(b.categories, map[string]any{
			"id": c.ID, "name": c.Name, "icon": c.Icon, "color": c.Color, "grouping": textOrNil(c.Grouping), "created_at": tsReq(c.CreatedAt),
		})
	}
	ord, err := q.ExportCategoryOrder(ctx, userID)
	if err != nil {
		return nil, err
	}
	b.categoryOrder = []map[string]any{}
	for _, o := range ord {
		b.categoryOrder = append(b.categoryOrder, map[string]any{"category_id": o.CategoryID, "sort_order": o.SortOrder})
	}

	tags, err := q.ExportTags(ctx, userID)
	if err != nil {
		return nil, err
	}
	b.tags = []map[string]any{}
	for _, t := range tags {
		b.tags = append(b.tags, map[string]any{
			"id": t.ID, "name": t.Name, "color": t.Color, "icon": t.Icon, "sort_order": t.SortOrder,
			"created_at": tsReq(t.CreatedAt), "updated_at": tsReq(t.UpdatedAt),
		})
	}

	plans, err := q.ExportPlannedPayments(ctx, userID)
	if err != nil {
		return nil, err
	}
	b.planned = []map[string]any{}
	for _, p := range plans {
		var n *int32
		if p.IntervalN.Valid {
			n = &p.IntervalN.Int32
		}
		b.planned = append(b.planned, map[string]any{
			"id": p.ID, "type": p.Type, "account_id": uuidOrNil(p.AccountID), "to_account_id": uuidOrNil(p.ToAccountID),
			"amount": p.Amount, "currency": cur(p.Currency), "to_amount": int8OrNil(p.ToAmount), "category_id": nilIfZero(p.CategoryID),
			"title": p.Title, "description": p.Description, "start_at": tsReq(p.StartAt), "one_time": p.OneTime,
			"interval_n": n, "interval_unit": textOrNil(p.IntervalUnit), "active": p.Active,
			"resolved_until": tsOrNil(p.ResolvedUntil), "tag_ids": uuidList(p.TagIds),
			"created_at": tsReq(p.CreatedAt), "updated_at": tsReq(p.UpdatedAt),
		})
	}

	buds, err := q.ExportBudgets(ctx, userID)
	if err != nil {
		return nil, err
	}
	b.budgets = []map[string]any{}
	for _, x := range buds {
		b.budgets = append(b.budgets, map[string]any{
			"id": x.ID, "name": x.Name, "amount": x.Amount, "currency": cur(x.Currency),
			"category_ids": uuidList(x.CategoryIds), "account_ids": uuidList(x.AccountIds), "sort_order": x.SortOrder,
			"created_at": tsReq(x.CreatedAt), "updated_at": tsReq(x.UpdatedAt),
		})
	}

	loans, err := q.ExportLoans(ctx, userID)
	if err != nil {
		return nil, err
	}
	recs, err := q.ExportLoanRecords(ctx, userID)
	if err != nil {
		return nil, err
	}
	recsOf := map[uuid.UUID][]map[string]any{}
	for _, r := range recs {
		id := uuid.UUID(r.LoanID.Bytes)
		recsOf[id] = append(recsOf[id], map[string]any{
			"id": r.ID, "loan_id": id, "type": r.Type, "amount": r.Amount, "note": r.Note, "occurred_at": tsReq(r.OccurredAt),
			"account_id": uuidOrNil(r.AccountID), "transaction_id": uuidOrNil(r.TransactionID),
			"created_at": tsReq(r.CreatedAt), "updated_at": tsReq(r.UpdatedAt),
		})
	}
	b.loans = []map[string]any{}
	for _, l := range loans {
		rs := recsOf[l.ID]
		if rs == nil {
			rs = []map[string]any{}
		}
		b.loans = append(b.loans, map[string]any{
			"id": l.ID, "name": l.Name, "type": l.Type, "amount": l.Amount, "currency": cur(l.Currency), "color": l.Color, "icon": l.Icon,
			"note": l.Note, "account_id": uuidOrNil(l.AccountID), "occurred_at": tsReq(l.OccurredAt), "sort_order": l.SortOrder,
			"created_at": tsReq(l.CreatedAt), "updated_at": tsReq(l.UpdatedAt), "records": rs,
		})
	}

	if err := b.loadGroups(ctx, q, userID); err != nil {
		return nil, err
	}
	return b, nil
}

// loadGroups fills `groups` (per group: its expenses and settlements, from the
// caller's point of view) and `data`, the flat expense list of the version 1 export.
func (b *backup) loadGroups(ctx context.Context, q *db.Queries, userID uuid.UUID) error {
	groups, err := q.ExportMyGroups(ctx, userID)
	if err != nil {
		return err
	}
	exps, err := q.ExportMyGroupExpenses(ctx, userID)
	if err != nil {
		return err
	}
	sets, err := q.ExportMyGroupSettlements(ctx, userID)
	if err != nil {
		return err
	}
	names := map[uuid.UUID]string{}
	expsOf := map[uuid.UUID][]map[string]any{}
	b.legacyData = []map[string]any{}
	for _, g := range groups {
		names[g.ID] = g.Name
	}
	for _, e := range exps {
		date := ""
		if e.ExpenseDate.Valid {
			date = e.ExpenseDate.Time.Format("2006-01-02")
		}
		expsOf[e.GroupID] = append(expsOf[e.GroupID], map[string]any{
			"id": e.ID, "description": e.Description, "category_id": nilIfZero(e.CategoryID), "category": e.Category,
			"paid_by_id": e.PaidByID, "paid_by": e.PaidBy, "amount": e.Amount, "currency": cur(e.Currency),
			"split_mode": e.SplitMode, "expense_date": date, "created_at": tsReq(e.CreatedAt), "notes": e.Notes,
			"payer_account_id": uuidOrNil(e.PayerAccountID), "my_share": e.MyShare,
		})
		// Version 1 shape, kept for clients that read "data".
		b.legacyData = append(b.legacyData, map[string]any{
			"id": e.ID, "group_id": e.GroupID, "group_name": names[e.GroupID], "description": e.Description,
			"category": e.Category, "paid_by": e.PaidBy, "amount": e.Amount, "currency": cur(e.Currency),
			"split_mode": e.SplitMode, "expense_date": date, "created_at": tsReq(e.CreatedAt),
		})
	}
	setsOf := map[uuid.UUID][]map[string]any{}
	for _, s := range sets {
		setsOf[s.GroupID] = append(setsOf[s.GroupID], map[string]any{
			"id": s.ID, "from_user_id": s.FromUser, "from_name": s.FromName, "to_user_id": s.ToUser, "to_name": s.ToName,
			"amount": s.Amount, "currency": cur(s.Currency), "note": s.Note, "settled_at": tsReq(s.SettledAt),
			"from_account_id": uuidOrNil(s.FromAccountID),
		})
	}
	b.groups = []map[string]any{}
	for _, g := range groups {
		e, s := expsOf[g.ID], setsOf[g.ID]
		if e == nil {
			e = []map[string]any{}
		}
		if s == nil {
			s = []map[string]any{}
		}
		b.groups = append(b.groups, map[string]any{
			"id": g.ID, "name": g.Name, "description": g.Description, "currency": cur(g.Currency), "group_type": g.GroupType,
			"role": g.Role, "archived_at": tsOrNil(g.ArchivedAt), "created_at": tsReq(g.CreatedAt),
			"expenses": e, "settlements": s,
		})
	}
	return nil
}

// ---------------------------------------------------------------- CSV exports

// csvSafe neutralises spreadsheet formula injection: cells starting with = + - @
// (or a tab / carriage return) get a leading apostrophe. Use it for text cells only.
func csvSafe(s string) string {
	if s == "" {
		return s
	}
	switch s[0] {
	case '\t', '\r', '\n':
		return "'" + s
	}
	// Spreadsheets ignore leading spaces/quotes before a formula.
	i := 0
	for i < len(s) && (s[i] == ' ' || s[i] == '\'' || s[i] == '"') {
		i++
	}
	if i < len(s) && strings.IndexByte("=+-@", s[i]) >= 0 {
		return "'" + s
	}
	return s
}

// minor and major render a stored amount (hundredths of the major unit for every
// currency) as minor units and as a decimal major-unit string.
func minor(v int64) string { return strconv.FormatInt(v, 10) }

func minorPtr(v pgtype.Int8) (string, string) {
	if !v.Valid {
		return "", ""
	}
	return minor(v.Int64), money(v.Int64)
}

// longWrite lifts the server's 30s write deadline for a streamed export.
func longWrite(w http.ResponseWriter) {
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(5 * time.Minute))
}

func newCSV(w http.ResponseWriter, filename string, header []string) *csv.Writer {
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", "attachment; filename=\""+filename+"\"")
	cw := csv.NewWriter(w)
	_ = cw.Write(header)
	return cw
}

// finishCSV flushes and, on a mid-stream failure, aborts the connection.
func finishCSV(cw *csv.Writer, err error, what string) {
	cw.Flush()
	if err == nil {
		err = cw.Error()
	}
	if err != nil {
		slog.Error("export: csv aborted", "error", err, "export", what)
		panic(http.ErrAbortHandler)
	}
}

// TransactionsCSV streams every wallet transaction (planned and adjustment/loan
// bookings included, newest first); group-share ledger entries are not part of it.
func (h *Handler) TransactionsCSV(w http.ResponseWriter, r *http.Request) {
	uid, _ := httpx.GetUserID(r.Context())
	userID, _ := uuid.Parse(uid)
	q := h.ensureQueries()
	if q == nil {
		httpx.WriteError(w, r, fmt.Errorf("no db queries available"))
		return
	}
	ctx := r.Context()
	fetch := func(cursorAt pgtype.Timestamptz, cursorID uuid.UUID) ([]db.ExportTransactionsCSVPageRow, error) {
		return q.ExportTransactionsCSVPage(ctx, db.ExportTransactionsCSVPageParams{UserID: userID, CursorAt: cursorAt, CursorID: pgUUID(cursorID), Lim: pageSize})
	}
	page, err := fetch(pgtype.Timestamptz{}, uuid.Nil)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	longWrite(w)
	cw := newCSV(w, "settlr-transactions.csv", []string{
		"id", "date", "planned", "type", "title", "description", "account", "to_account", "category", "tags",
		"amount_minor", "amount", "currency", "to_amount_minor", "to_amount", "to_currency", "source",
	})
	rc := http.NewResponseController(w)
	for len(page) > 0 {
		for _, t := range page {
			toMinor, toMajor := minorPtr(t.ToAmount)
			toCur := cur(t.ToCurrency)
			_ = cw.Write([]string{
				t.ID.String(), t.SortAt.Time.UTC().Format(time.RFC3339), strconv.FormatBool(!t.OccurredAt.Valid), t.Type,
				csvSafe(t.Title), csvSafe(t.Description), csvSafe(t.AccountName), csvSafe(t.ToAccountName), csvSafe(t.CategoryName), csvSafe(t.TagNames),
				minor(t.Amount), money(t.Amount), cur(t.Currency), toMinor, toMajor, toCur, t.Source,
			})
		}
		cw.Flush()
		_ = rc.Flush()
		if len(page) < pageSize {
			break
		}
		last := page[len(page)-1]
		if page, err = fetch(last.SortAt, last.ID); err != nil {
			break
		}
	}
	finishCSV(cw, err, "transactions")
}

func (h *Handler) AccountsCSV(w http.ResponseWriter, r *http.Request) {
	uid, _ := httpx.GetUserID(r.Context())
	userID, _ := uuid.Parse(uid)
	q := h.ensureQueries()
	if q == nil {
		httpx.WriteError(w, r, fmt.Errorf("no db queries available"))
		return
	}
	accs, err := q.ExportAccounts(r.Context(), userID)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	cw := newCSV(w, "settlr-accounts.csv", []string{"id", "name", "currency", "color", "icon", "include_in_balance", "is_default", "archived", "created_at"})
	for _, a := range accs {
		_ = cw.Write([]string{
			a.ID.String(), csvSafe(a.Name), cur(a.Currency), csvSafe(a.Color), csvSafe(a.Icon), strconv.FormatBool(a.IncludeInBalance),
			strconv.FormatBool(a.IsDefault), strconv.FormatBool(a.ArchivedAt.Valid), tsReq(a.CreatedAt).Format(time.RFC3339),
		})
	}
	finishCSV(cw, nil, "accounts")
}

func (h *Handler) BudgetsCSV(w http.ResponseWriter, r *http.Request) {
	uid, _ := httpx.GetUserID(r.Context())
	userID, _ := uuid.Parse(uid)
	q := h.ensureQueries()
	if q == nil {
		httpx.WriteError(w, r, fmt.Errorf("no db queries available"))
		return
	}
	ctx := r.Context()
	buds, err := q.ExportBudgets(ctx, userID)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	accs, err := q.ExportAccounts(ctx, userID)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	accName := map[uuid.UUID]string{}
	for _, a := range accs {
		accName[a.ID] = a.Name
	}
	var catIDs []pgtype.UUID
	for _, b := range buds {
		catIDs = append(catIDs, b.CategoryIds...)
	}
	catName := map[uuid.UUID]string{}
	if len(catIDs) > 0 {
		rows, err := q.ExportCategoryNames(ctx, catIDs)
		if err != nil {
			httpx.WriteError(w, r, err)
			return
		}
		for _, c := range rows {
			catName[c.ID] = c.Name
		}
	}
	join := func(ids []uuid.UUID, names map[uuid.UUID]string) string {
		var parts []string
		for _, id := range ids {
			if n, ok := names[id]; ok {
				parts = append(parts, n)
			}
		}
		return strings.Join(parts, "; ")
	}
	cw := newCSV(w, "settlr-budgets.csv", []string{"id", "name", "amount_minor", "amount", "currency", "categories", "accounts"})
	for _, b := range buds {
		_ = cw.Write([]string{
			b.ID.String(), csvSafe(b.Name), minor(b.Amount), money(b.Amount), cur(b.Currency),
			csvSafe(join(uuidList(b.CategoryIds), catName)), csvSafe(join(uuidList(b.AccountIds), accName)),
		})
	}
	finishCSV(cw, nil, "budgets")
}

func (h *Handler) LoansCSV(w http.ResponseWriter, r *http.Request) {
	uid, _ := httpx.GetUserID(r.Context())
	userID, _ := uuid.Parse(uid)
	q := h.ensureQueries()
	if q == nil {
		httpx.WriteError(w, r, fmt.Errorf("no db queries available"))
		return
	}
	ctx := r.Context()
	loans, err := q.ExportLoans(ctx, userID)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	accs, err := q.ExportAccounts(ctx, userID)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	accName := map[uuid.UUID]string{}
	for _, a := range accs {
		accName[a.ID] = a.Name
	}
	ids := make([]pgtype.UUID, len(loans))
	for i, l := range loans {
		ids[i] = pgtype.UUID{Bytes: l.ID, Valid: true}
	}
	type totals struct{ inc, dec int64 }
	tot := map[uuid.UUID]totals{}
	if len(ids) > 0 {
		rows, err := q.LoanRecordTotals(ctx, ids)
		if err != nil {
			httpx.WriteError(w, r, err)
			return
		}
		for _, t := range rows {
			tot[uuid.UUID(t.LoanID.Bytes)] = totals{t.Increased, t.Decreased}
		}
	}
	cw := newCSV(w, "settlr-loans.csv", []string{
		"id", "name", "type", "amount_minor", "amount", "currency", "total_minor", "total", "paid_minor", "paid",
		"remaining_minor", "remaining", "paid_off", "account", "date", "note",
	})
	for _, l := range loans {
		t := tot[l.ID]
		total := l.Amount + t.inc
		remaining := max(total-t.dec, 0)
		account := ""
		if l.AccountID.Valid {
			account = accName[uuid.UUID(l.AccountID.Bytes)]
		}
		_ = cw.Write([]string{
			l.ID.String(), csvSafe(l.Name), l.Type, minor(l.Amount), money(l.Amount), cur(l.Currency),
			minor(total), money(total), minor(t.dec), money(t.dec), minor(remaining), money(remaining),
			strconv.FormatBool(t.dec >= total), csvSafe(account), tsReq(l.OccurredAt).Format(time.RFC3339), csvSafe(l.Note),
		})
	}
	finishCSV(cw, nil, "loans")
}
