package rates

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/settlr-org/settlr-api/internal/money"
)

// Provider returns the latest exchange rates for base: 1 base = rates[quote] quote.
type Provider interface {
	Latest(ctx context.Context, base string) (map[string]float64, error)
}

// Default upstream endpoints.
const (
	DefaultFrankfurterURL = "https://api.frankfurter.app"
	DefaultFallbackURL    = "https://api.exchangerate-api.com/v4/latest"
)

// HTTPProvider fetches rates from Frankfurter (ECB reference rates, ~30
// currencies, supports historical dates) and from exchangerate-api (~160
// currencies incl. NPR, latest only) and merges them: Frankfurter wins where both
// quote a currency, the fallback fills the currencies Frankfurter lacks. Without
// the merge, ?base=USD (served by Frankfurter) lacks NPR while ?base=NPR (which
// Frankfurter rejects, so the fallback serves it) includes USD.
type HTTPProvider struct {
	Client *http.Client
	// FrankfurterURL and FallbackURL override the upstream base URLs (tests).
	FrankfurterURL string
	FallbackURL    string
}

// Result is one rate table.
type Result struct {
	Base   string
	Date   string
	Rates  map[string]float64
	Source string
}

func (p *HTTPProvider) client() *http.Client {
	if p.Client != nil {
		return p.Client
	}
	return &http.Client{Timeout: 5 * time.Second}
}

func orDefault(s, def string) string {
	if s != "" {
		return strings.TrimRight(s, "/")
	}
	return def
}

func (p *HTTPProvider) Latest(ctx context.Context, base string) (map[string]float64, error) {
	res, err := p.Rates(ctx, base, "")
	if err != nil {
		return nil, err
	}
	return res.Rates, nil
}

// Rates returns base's rate table, for date (YYYY-MM-DD) when set. Historical
// tables come from Frankfurter only (the fallback has no history); if Frankfurter
// cannot serve the base, the fallback's latest table is returned instead.
func (p *HTTPProvider) Rates(ctx context.Context, base, date string) (Result, error) {
	base = strings.ToUpper(strings.TrimSpace(base))
	fURL := orDefault(p.FrankfurterURL, DefaultFrankfurterURL)
	if date != "" {
		fURL += "/" + date + "?from=" + base
	} else {
		fURL += "/latest?from=" + base
	}
	xURL := orDefault(p.FallbackURL, DefaultFallbackURL) + "/" + base

	var (
		wg         sync.WaitGroup
		fRes, xRes Result
		fErr, xErr error
	)
	wg.Add(2)
	go func() { defer wg.Done(); fRes, fErr = p.fetch(ctx, fURL) }()
	go func() { defer wg.Done(); xRes, xErr = p.fetch(ctx, xURL) }()
	wg.Wait()

	fOK := fErr == nil && len(fRes.Rates) > 0
	xOK := xErr == nil && len(xRes.Rates) > 0
	switch {
	case fOK && xOK && date == "":
		merged := make(map[string]float64, len(xRes.Rates))
		maps.Copy(merged, xRes.Rates)
		maps.Copy(merged, fRes.Rates)
		delete(merged, base)
		return Result{Base: base, Date: fRes.Date, Rates: merged, Source: "frankfurter+exchangerate-api"}, nil
	case fOK:
		fRes.Base, fRes.Source = base, "frankfurter"
		return fRes, nil
	case xOK:
		delete(xRes.Rates, base)
		xRes.Base, xRes.Source = base, "exchangerate-api"
		return xRes, nil
	}
	if fErr == nil {
		fErr = xErr
	}
	if fErr == nil {
		fErr = fmt.Errorf("no rates for %s", base)
	}
	return Result{}, fErr
}

func (p *HTTPProvider) fetch(ctx context.Context, url string) (Result, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return Result{}, err
	}
	resp, err := p.client().Do(req)
	if err != nil {
		return Result{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Result{}, fmt.Errorf("rates %s: status %d", url, resp.StatusCode)
	}
	var data struct {
		Rates map[string]float64 `json:"rates"`
		Date  string             `json:"date"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return Result{}, err
	}
	return Result{Date: data.Date, Rates: data.Rates}, nil
}

// Pivots are the currencies tried as an intermediate leg when neither a direct
// nor an inverse rate is known (A→EUR→B, then A→USD→B).
var Pivots = []string{"EUR", "USD"}

// Converter converts minor-unit amounts between currencies using a user's manual
// overrides first and then the provider's rates, cached per base currency per UTC day.
// A nil Converter or nil Provider only knows identity and override conversions.
type Converter struct {
	Provider Provider
	Now      func() time.Time

	mu    sync.Mutex
	cache map[string]cachedRates
}

type cachedRates struct {
	rates   map[string]float64
	err     error
	fetched time.Time
}

// failedFetchTTL keeps a failing provider from being hit on every request.
const failedFetchTTL = 10 * time.Minute

func NewConverter(p Provider) *Converter {
	return &Converter{Provider: p}
}

func (c *Converter) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func (c *Converter) providerRates(ctx context.Context, base string) map[string]float64 {
	if c == nil || c.Provider == nil {
		return nil
	}
	now := c.now().UTC()
	key := base + "|" + now.Format("2006-01-02")
	c.mu.Lock()
	if c.cache == nil {
		c.cache = make(map[string]cachedRates)
	}
	if e, ok := c.cache[key]; ok && (e.err == nil || now.Sub(e.fetched) < failedFetchTTL) {
		c.mu.Unlock()
		return e.rates
	}
	c.mu.Unlock()
	rates, err := c.Provider.Latest(ctx, base)
	c.mu.Lock()
	c.cache[key] = cachedRates{rates: rates, err: err, fetched: now}
	c.mu.Unlock()
	if err != nil {
		return nil
	}
	return rates
}

// Overrides maps "BASE/QUOTE" to a user-chosen rate (1 BASE = rate QUOTE).
// Keys are upper-case currency codes.
type Overrides map[string]float64

func OverrideKey(base, quote string) string {
	return strings.ToUpper(strings.TrimSpace(base)) + "/" + strings.ToUpper(strings.TrimSpace(quote))
}

// Rate returns how many `to` one `from` buys. Lookup order:
//  1. a user override for from/to, or the inverse of one for to/from;
//  2. the provider's from table, or the inverse of its to table;
//  3. a cross rate via a pivot (EUR, then USD), each leg resolved by 1–2.
func (c *Converter) Rate(ctx context.Context, ov Overrides, from, to string) (float64, bool) {
	from, to = strings.ToUpper(strings.TrimSpace(from)), strings.ToUpper(strings.TrimSpace(to))
	if from == to {
		return 1, true
	}
	if r, ok := c.direct(ctx, ov, from, to); ok {
		return r, true
	}
	for _, p := range Pivots {
		if p == from || p == to {
			continue
		}
		a, ok := c.direct(ctx, ov, from, p)
		if !ok {
			continue
		}
		if b, ok := c.direct(ctx, ov, p, to); ok {
			return a * b, true
		}
	}
	return 0, false
}

// direct resolves from→to from overrides, then from the provider, each either
// directly or by inverting the opposite quote.
func (c *Converter) direct(ctx context.Context, ov Overrides, from, to string) (float64, bool) {
	if r, ok := ov[OverrideKey(from, to)]; ok && r > 0 {
		return r, true
	}
	if r, ok := ov[OverrideKey(to, from)]; ok && r > 0 {
		return 1 / r, true
	}
	if r, ok := c.providerRates(ctx, from)[to]; ok && r > 0 {
		return r, true
	}
	if r, ok := c.providerRates(ctx, to)[from]; ok && r > 0 {
		return 1 / r, true
	}
	return 0, false
}

// Convert converts a stored amount of `from` into `to` (see money.ConvertMinor:
// rounded half away from zero to the target currency's real precision).
func (c *Converter) Convert(ctx context.Context, ov Overrides, amount int64, from, to string) (int64, bool) {
	from, to = strings.ToUpper(strings.TrimSpace(from)), strings.ToUpper(strings.TrimSpace(to))
	if from == to {
		return amount, true
	}
	r, ok := c.Rate(ctx, ov, from, to)
	if !ok {
		return 0, false
	}
	return money.ConvertMinor(amount, r, from, to), true
}
