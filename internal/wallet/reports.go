package wallet

import (
	"context"
	"fmt"
	"math/bits"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	db "github.com/settlr-org/settlr-api/internal/db"
	"github.com/settlr-org/settlr-api/internal/export"
	"github.com/settlr-org/settlr-api/internal/httpx"
)

// Report limits.
const (
	maxReportYears = 10  // widest from..to accepted
	maxTrendBucket = 400 // most buckets one trend response may contain
	csvPageSize    = 1000
)

// reportFilters parses the /ledger filter set for a report endpoint and validates
// the date range (to after from, at most maxReportYears wide).
func reportFilters(r *http.Request) (filters, *time.Time, *time.Time, string) {
	f, msg := parseFilters(r, true)
	if msg != "" {
		return f, nil, nil, msg
	}
	from, to := tsPtr(f.from), tsPtr(f.to)
	if from != nil && to != nil {
		if !to.After(*from) {
			return f, nil, nil, "to must be after from"
		}
		if to.After(from.AddDate(maxReportYears, 0, 1)) {
			return f, nil, nil, fmt.Sprintf("the date range is limited to %d years", maxReportYears)
		}
	}
	// Pagination does not apply to aggregates or the export.
	f.cursorAt, f.cursorID = pgtype.Timestamptz{}, pgtype.UUID{}
	f.normalize()
	return f, from, to, ""
}

// normalize replaces nil slices by empty ones (sqlc binds nil as SQL NULL, which breaks cardinality()).
func (f *filters) normalize() {
	for _, s := range []*[]string{&f.kinds, &f.types, &f.sources, &f.exclSources, &f.qTerms, &f.exclQTerms} {
		if *s == nil {
			*s = []string{}
		}
	}
	for _, s := range []*[]pgtype.UUID{&f.accounts, &f.categories, &f.groups, &f.tags, &f.exclTags} {
		if *s == nil {
			*s = []pgtype.UUID{}
		}
	}
}

func (f filters) totalsParams(userID uuid.UUID) db.ReportTotalsParams {
	return db.ReportTotalsParams{
		UserID: userID, Planned: f.planned, FromAt: f.from, ToAt: f.to, Kinds: f.kinds, Types: f.types,
		Sources: f.sources, ExclSources: f.exclSources, AccountIds: f.accounts, CategoryIds: f.categories,
		CategoryNone: f.catNone, GroupIds: f.groups, TagIds: f.tags, ExclTagIds: f.exclTags,
		QTerms: f.qTerms, ExclQTerms: f.exclQTerms, MinAmount: f.min, MaxAmount: f.max,
	}
}

func (f filters) categoryParams(userID uuid.UUID, breakdown string) db.ReportByCategoryParams {
	return db.ReportByCategoryParams{
		UserID: userID, Planned: f.planned, FromAt: f.from, ToAt: f.to, Kinds: f.kinds, Types: f.types,
		Sources: f.sources, ExclSources: f.exclSources, AccountIds: f.accounts, CategoryIds: f.categories,
		CategoryNone: f.catNone, GroupIds: f.groups, TagIds: f.tags, ExclTagIds: f.exclTags,
		QTerms: f.qTerms, ExclQTerms: f.exclQTerms, MinAmount: f.min, MaxAmount: f.max, Breakdown: breakdown,
	}
}

func (f filters) accountParams(userID uuid.UUID) db.ReportByAccountParams {
	return db.ReportByAccountParams(f.totalsParams(userID))
}

func (f filters) trendParams(userID uuid.UUID, bucket string, startDay int) db.ReportTrendParams {
	t := f.totalsParams(userID)
	return db.ReportTrendParams{
		Bucket: bucket, StartDay: int32(startDay),
		UserID: userID, Planned: t.Planned, FromAt: t.FromAt, ToAt: t.ToAt, Kinds: t.Kinds, Types: t.Types,
		Sources: t.Sources, ExclSources: t.ExclSources, AccountIds: t.AccountIds, CategoryIds: t.CategoryIds,
		CategoryNone: t.CategoryNone, GroupIds: t.GroupIds, TagIds: t.TagIds, ExclTagIds: t.ExclTagIds,
		QTerms: t.QTerms, ExclQTerms: t.ExclQTerms, MinAmount: t.MinAmount, MaxAmount: t.MaxAmount,
	}
}

// flows accumulates converted income/expense/transfer legs.
type flows struct {
	income, expense, tIn, tOut int64
}

func (a *flows) net(transfersAsFlow bool) int64 {
	if transfersAsFlow {
		return a.income + a.tIn - a.expense - a.tOut
	}
	return a.income - a.expense
}

func (a *flows) incomeTotal(asFlow bool) int64 {
	if asFlow {
		return a.income + a.tIn
	}
	return a.income
}

func (a *flows) expenseTotal(asFlow bool) int64 {
	if asFlow {
		return a.expense + a.tOut
	}
	return a.expense
}

// conv converts amount from currency `from` to `to`, recording a missing rate.
func (f *fx) conv(ctx context.Context, amount int64, from, to string) int64 {
	v, _ := f.convert(ctx, amount, from, to)
	return v
}

// percentTenths distributes 1000 units (100.0 %) over amounts proportionally with the
// largest-remainder method, so the shares always sum to exactly 100.0. Ties go to the
// earlier (larger) item. All-zero input yields zeros.
func percentTenths(amounts []int64) []int64 {
	out := make([]int64, len(amounts))
	var total int64
	for _, a := range amounts {
		if a > 0 {
			total += a
		}
	}
	if total == 0 {
		return out
	}
	type rem struct {
		i int
		r int64
	}
	rems := make([]rem, 0, len(amounts))
	var used int64
	for i, a := range amounts {
		if a <= 0 {
			continue
		}
		// a * 1000 / total in 128-bit-safe fashion: a and total fit in int64 and a <= total.
		q, r := mulDiv(a, 1000, total)
		out[i] = q
		used += q
		rems = append(rems, rem{i, r})
	}
	sort.SliceStable(rems, func(x, y int) bool { return rems[x].r > rems[y].r })
	for k := int64(0); k < 1000-used && int(k) < len(rems); k++ {
		out[rems[k].i]++
	}
	return out
}

// mulDiv returns floor(a*b/c) and a*b mod c for a, b > 0, c >= a (so the quotient fits),
// without overflowing int64.
func mulDiv(a, b, c int64) (int64, int64) {
	hi, lo := bits.Mul64(uint64(a), uint64(b))
	q, r := bits.Div64(hi, lo, uint64(c))
	return int64(q), int64(r)
}

var uncategorized = struct{ name, icon, color string }{"Uncategorized", "tag", "#6B7280"}

// Report aggregates the ledger entries matching the /ledger filter set in the user's
// base currency: income, expense, transfers, a category breakdown and per-account totals.
func (h *Handler) Report(w http.ResponseWriter, r *http.Request) {
	userID := currentUser(r)
	ctx := r.Context()
	f, from, to, msg := reportFilters(r)
	if msg != "" {
		validation(w, msg)
		return
	}
	breakdown := strings.ToUpper(strings.TrimSpace(r.URL.Query().Get("breakdown")))
	switch breakdown {
	case "":
		breakdown = "EXPENSE"
	case "EXPENSE", "INCOME":
	default:
		validation(w, "breakdown must be expense or income")
		return
	}
	settings, err := h.loadSettings(ctx, userID)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	asFlow := settings.TreatTransfersAsFlow
	fxc, err := h.newFX(ctx, userID)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	q := h.q()

	totals, err := q.ReportTotals(ctx, f.totalsParams(userID))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	var tot flows
	count := 0
	for _, row := range totals {
		tot.income += fxc.conv(ctx, row.Income, row.Currency, fxc.base)
		tot.expense += fxc.conv(ctx, row.Expense, row.Currency, fxc.base)
		tot.tIn += fxc.conv(ctx, row.TransfersIn, row.Currency, fxc.base)
		tot.tOut += fxc.conv(ctx, row.TransfersOut, row.Currency, fxc.base)
		count += int(row.Entries)
		if asFlow {
			count += int(row.TransferEntries)
		}
	}

	catRows, err := q.ReportByCategory(ctx, f.categoryParams(userID, breakdown))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	type catAgg struct {
		id                string
		name, icon, color string
		amount            int64
		count             int
		nilID             bool
	}
	cats := map[uuid.UUID]*catAgg{}
	for _, row := range catRows {
		a := cats[row.CategoryID]
		if a == nil {
			a = &catAgg{name: row.Name, icon: row.Icon, color: row.Color, nilID: row.CategoryID == uuid.Nil}
			if a.nilID {
				a.name, a.icon, a.color = uncategorized.name, uncategorized.icon, uncategorized.color
			}
			cats[row.CategoryID] = a
		}
		a.amount += fxc.conv(ctx, row.Amount, row.Currency, fxc.base)
		a.count += int(row.Entries)
	}
	catList := make([]uuid.UUID, 0, len(cats))
	for id := range cats {
		catList = append(catList, id)
	}
	sort.Slice(catList, func(i, j int) bool {
		a, b := cats[catList[i]], cats[catList[j]]
		if a.amount != b.amount {
			return a.amount > b.amount
		}
		if a.name != b.name {
			return a.name < b.name
		}
		return catList[i].String() < catList[j].String()
	})
	amounts := make([]int64, len(catList))
	for i, id := range catList {
		amounts[i] = cats[id].amount
	}
	pct := percentTenths(amounts)
	byCategory := make([]map[string]any, 0, len(catList))
	for i, id := range catList {
		a := cats[id]
		var cid *uuid.UUID
		if id != uuid.Nil {
			c := id
			cid = &c
		}
		byCategory = append(byCategory, map[string]any{
			"category_id": cid, "name": a.name, "icon": a.icon, "color": a.color,
			"amount": a.amount, "count": a.count, "percent": float64(pct[i]) / 10,
		})
	}

	accRows, err := q.ReportByAccount(ctx, f.accountParams(userID))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	type accAgg struct {
		name, currency string
		own, base      flows
	}
	accs := map[uuid.UUID]*accAgg{}
	for _, row := range accRows {
		a := accs[row.AccountID]
		if a == nil {
			a = &accAgg{name: row.Name, currency: row.AccountCurrency}
			if row.AccountID == uuid.Nil || a.currency == "" {
				a.name, a.currency = "No account", fxc.base
			}
			accs[row.AccountID] = a
		}
		a.own.income += fxc.conv(ctx, row.Income, row.Currency, a.currency)
		a.own.expense += fxc.conv(ctx, row.Expense, row.Currency, a.currency)
		a.own.tIn += fxc.conv(ctx, row.TransfersIn, row.Currency, a.currency)
		a.own.tOut += fxc.conv(ctx, row.TransfersOut, row.Currency, a.currency)
		a.base.income += fxc.conv(ctx, row.Income, row.Currency, fxc.base)
		a.base.expense += fxc.conv(ctx, row.Expense, row.Currency, fxc.base)
		a.base.tIn += fxc.conv(ctx, row.TransfersIn, row.Currency, fxc.base)
		a.base.tOut += fxc.conv(ctx, row.TransfersOut, row.Currency, fxc.base)
	}
	accIDs := make([]uuid.UUID, 0, len(accs))
	for id := range accs {
		accIDs = append(accIDs, id)
	}
	sort.Slice(accIDs, func(i, j int) bool {
		a, b := accs[accIDs[i]], accs[accIDs[j]]
		va := a.base.incomeTotal(asFlow) + a.base.expenseTotal(asFlow)
		vb := b.base.incomeTotal(asFlow) + b.base.expenseTotal(asFlow)
		if va != vb {
			return va > vb
		}
		if a.name != b.name {
			return a.name < b.name
		}
		return accIDs[i].String() < accIDs[j].String()
	})
	byAccount := make([]map[string]any, 0, len(accIDs))
	for _, id := range accIDs {
		a := accs[id]
		var aid *uuid.UUID
		if id != uuid.Nil {
			c := id
			aid = &c
		}
		byAccount = append(byAccount, map[string]any{
			"account_id": aid, "name": a.name, "currency": a.currency,
			"income": a.own.incomeTotal(asFlow), "expense": a.own.expenseTotal(asFlow), "net": a.own.net(asFlow),
			"base_income": a.base.incomeTotal(asFlow), "base_expense": a.base.expenseTotal(asFlow), "base_net": a.base.net(asFlow),
		})
	}

	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"currency": fxc.base, "from": from, "to": to,
		"income": tot.incomeTotal(asFlow), "expense": tot.expenseTotal(asFlow), "net": tot.net(asFlow),
		"transfers_in": tot.tIn, "transfers_out": tot.tOut,
		"count": count, "breakdown": strings.ToLower(breakdown),
		"by_category": byCategory, "by_account": byAccount,
		"treat_transfers_as_flow": asFlow,
		"hide_adjustments":        hideAdjustments(f),
		"missing_rates":           fxc.missingList(),
	})
}

// hideAdjustments reports whether adjustments were explicitly filtered out of the entries
// list (they never count as income or expense in a report either way).
func hideAdjustments(f filters) bool {
	for _, s := range f.exclSources {
		if s == "ADJUSTMENT" {
			return true
		}
	}
	return false
}

// bucketStart returns the start of the bucket (UTC) containing t.
func bucketStart(t time.Time, bucket string, startDay int) time.Time {
	t = t.UTC()
	switch bucket {
	case "day":
		return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
	case "week":
		d := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
		return d.AddDate(0, 0, -((int(d.Weekday()) + 6) % 7)) // Monday
	}
	s := time.Date(t.Year(), t.Month(), startDay, 0, 0, 0, 0, time.UTC)
	if t.Before(s) {
		s = s.AddDate(0, -1, 0)
	}
	return s
}

func nextBucket(start time.Time, bucket string) time.Time {
	switch bucket {
	case "day":
		return start.AddDate(0, 0, 1)
	case "week":
		return start.AddDate(0, 0, 7)
	}
	return start.AddDate(0, 1, 0) // start day <= 28: never clamps
}

// Trend returns income/expense/net per bucket, zero-filled across [from, to).
// Buckets are UTC: days at midnight, weeks start on Monday, months start on the
// user's month_start_day. The first and last buckets are whole buckets even when
// from/to fall inside them (entries outside [from, to) are not counted).
func (h *Handler) Trend(w http.ResponseWriter, r *http.Request) {
	userID := currentUser(r)
	ctx := r.Context()
	f, from, to, msg := reportFilters(r)
	if msg == "" && (from == nil || to == nil) {
		msg = "from and to are required"
	}
	bucket := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("bucket")))
	if msg == "" {
		switch bucket {
		case "":
			bucket = "month"
		case "day", "week", "month":
		default:
			msg = "bucket must be day, week or month"
		}
	}
	if msg != "" {
		validation(w, msg)
		return
	}
	settings, err := h.loadSettings(ctx, userID)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	startDay := int(settings.MonthStartDay)
	var starts []time.Time
	for s := bucketStart(*from, bucket, startDay); s.Before(*to); s = nextBucket(s, bucket) {
		if len(starts) == maxTrendBucket {
			validation(w, fmt.Sprintf("the range spans more than %d %s buckets; use a larger bucket or a shorter range", maxTrendBucket, bucket))
			return
		}
		starts = append(starts, s)
	}
	idx := make(map[int64]int, len(starts))
	for i, s := range starts {
		idx[s.Unix()] = i
	}
	asFlow := settings.TreatTransfersAsFlow
	fxc, err := h.newFX(ctx, userID)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	rows, err := h.q().ReportTrend(ctx, f.trendParams(userID, bucket, startDay))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	acc := make([]flows, len(starts))
	for _, row := range rows {
		i, ok := idx[row.BucketStart.Time.UTC().Unix()]
		if !ok {
			continue
		}
		acc[i].income += fxc.conv(ctx, row.Income, row.Currency, fxc.base)
		acc[i].expense += fxc.conv(ctx, row.Expense, row.Currency, fxc.base)
		acc[i].tIn += fxc.conv(ctx, row.TransfersIn, row.Currency, fxc.base)
		acc[i].tOut += fxc.conv(ctx, row.TransfersOut, row.Currency, fxc.base)
	}
	buckets := make([]map[string]any, len(starts))
	for i, s := range starts {
		buckets[i] = map[string]any{
			"start": s, "end": nextBucket(s, bucket),
			"income": acc[i].incomeTotal(asFlow), "expense": acc[i].expenseTotal(asFlow), "net": acc[i].net(asFlow),
		}
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"currency": fxc.base, "from": from, "to": to, "bucket": bucket,
		"buckets": buckets, "missing_rates": fxc.missingList(),
	})
}

var dateOnly = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)

// reportFilename is settlr-report-<from>-<to>.csv (dates, "all" for an open end).
func reportFilename(r *http.Request, from, to *time.Time) string {
	part := func(name string, t *time.Time) string {
		if raw := strings.TrimSpace(r.URL.Query().Get(name)); dateOnly.MatchString(raw) {
			return raw
		}
		if t == nil {
			return "all"
		}
		return t.UTC().Format("2006-01-02")
	}
	return "settlr-report-" + part("from", from) + "-" + part("to", to) + ".csv"
}

// ReportCSV streams the ledger entries matching the filters (wallet transactions and
// group shares/settlements, flagged by the kind column), newest first, in pages.
func (h *Handler) ReportCSV(w http.ResponseWriter, r *http.Request) {
	userID := currentUser(r)
	ctx := r.Context()
	f, from, to, msg := reportFilters(r)
	if msg != "" {
		validation(w, msg)
		return
	}
	fxc, err := h.newFX(ctx, userID)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	q := h.q()
	fetch := func(cursorAt pgtype.Timestamptz, cursorID pgtype.UUID) ([]db.LedgerEntry, error) {
		return q.ListLedgerEntries(ctx, db.ListLedgerEntriesParams{
			UserID: userID, Planned: f.planned, FromAt: f.from, ToAt: f.to, Kinds: f.kinds, Types: f.types,
			Sources: f.sources, ExclSources: f.exclSources, AccountIds: f.accounts, CategoryIds: f.categories,
			CategoryNone: f.catNone, GroupIds: f.groups, TagIds: f.tags, ExclTagIds: f.exclTags,
			QTerms: f.qTerms, ExclQTerms: f.exclQTerms, MinAmount: f.min, MaxAmount: f.max,
			CursorAt: cursorAt, CursorID: cursorID, Lim: csvPageSize,
		})
	}
	page, err := fetch(pgtype.Timestamptz{}, pgtype.UUID{})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	export.LongWrite(w)
	cw := export.NewCSV(w, reportFilename(r, from, to), []string{
		"date", "kind", "type", "title", "description", "account", "to_account", "category", "tags",
		"amount_minor", "amount", "currency", "base_amount", "base_currency", "group", "source",
	})
	rc := http.NewResponseController(w)
	for len(page) > 0 {
		accNames, catNames, tags, nerr := h.csvLookups(ctx, userID, page)
		if nerr != nil {
			err = nerr
			break
		}
		for _, e := range page {
			cur := strings.TrimSpace(e.Currency)
			baseAmount := ""
			if v, ok := fxc.convert(ctx, e.Amount, cur, fxc.base); ok {
				baseAmount = export.Money(v)
			}
			var names []string
			for _, t := range tags[e.ID] {
				names = append(names, t.Name)
			}
			group := ""
			if e.GroupName.Valid {
				group = e.GroupName.String
			}
			_ = cw.Write([]string{
				e.SortAt.Time.UTC().Format(time.RFC3339), e.Kind, e.Type,
				export.CSVSafe(e.Title), export.CSVSafe(e.Description),
				export.CSVSafe(accNames[uuidOf(e.AccountID)]), export.CSVSafe(accNames[uuidOf(e.ToAccountID)]),
				export.CSVSafe(catNames[e.CategoryID]), export.CSVSafe(strings.Join(names, "; ")),
				strconv.FormatInt(e.Amount, 10), export.Money(e.Amount), cur, baseAmount, fxc.base,
				export.CSVSafe(group), e.Source,
			})
		}
		cw.Flush()
		_ = rc.Flush()
		if len(page) < csvPageSize {
			break
		}
		last := page[len(page)-1]
		if page, err = fetch(last.SortAt, pu(last.ID)); err != nil {
			break
		}
	}
	export.FinishCSV(cw, err, "report")
}

func uuidOf(u pgtype.UUID) uuid.UUID {
	if !u.Valid {
		return uuid.Nil
	}
	return uuid.UUID(u.Bytes)
}

// csvLookups loads the account/category names and tags the page's entries refer to.
func (h *Handler) csvLookups(ctx context.Context, userID uuid.UUID, page []db.LedgerEntry) (map[uuid.UUID]string, map[uuid.UUID]string, map[uuid.UUID][]tagRef, error) {
	accSet, catSet := map[uuid.UUID]bool{}, map[uuid.UUID]bool{}
	var txIDs []uuid.UUID
	for _, e := range page {
		if id := uuidOf(e.AccountID); id != uuid.Nil {
			accSet[id] = true
		}
		if id := uuidOf(e.ToAccountID); id != uuid.Nil {
			accSet[id] = true
		}
		if e.CategoryID != uuid.Nil {
			catSet[e.CategoryID] = true
		}
		if e.Kind == "TRANSACTION" {
			txIDs = append(txIDs, e.ID)
		}
	}
	keys := func(m map[uuid.UUID]bool) []uuid.UUID {
		out := make([]uuid.UUID, 0, len(m))
		for id := range m {
			out = append(out, id)
		}
		return out
	}
	accNames, catNames := map[uuid.UUID]string{}, map[uuid.UUID]string{}
	if len(accSet) > 0 {
		rows, err := h.q().ReportAccountNames(ctx, db.ReportAccountNamesParams{UserID: userID, Ids: pgUUIDs(keys(accSet))})
		if err != nil {
			return nil, nil, nil, err
		}
		for _, row := range rows {
			accNames[row.ID] = row.Name
		}
	}
	if len(catSet) > 0 {
		rows, err := h.q().ReportCategoryNames(ctx, db.ReportCategoryNamesParams{UserID: userID, Ids: pgUUIDs(keys(catSet))})
		if err != nil {
			return nil, nil, nil, err
		}
		for _, row := range rows {
			catNames[row.ID] = row.Name
		}
	}
	tags, err := h.tagsFor(ctx, txIDs)
	if err != nil {
		return nil, nil, nil, err
	}
	return accNames, catNames, tags, nil
}
