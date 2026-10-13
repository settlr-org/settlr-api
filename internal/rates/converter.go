package rates

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Provider returns the latest exchange rates for base: 1 base = rates[quote] quote.
type Provider interface {
	Latest(ctx context.Context, base string) (map[string]float64, error)
}

// HTTPProvider fetches rates from Frankfurter and falls back to exchangerate-api
// (which also covers currencies Frankfurter lacks, e.g. NPR).
type HTTPProvider struct {
	Client *http.Client
}

func (p *HTTPProvider) client() *http.Client {
	if p.Client != nil {
		return p.Client
	}
	return &http.Client{Timeout: 5 * time.Second}
}

func (p *HTTPProvider) Latest(ctx context.Context, base string) (map[string]float64, error) {
	base = strings.ToUpper(base)
	urls := []string{
		"https://api.frankfurter.app/latest?from=" + base,
		"https://api.exchangerate-api.com/v4/latest/" + base,
	}
	var lastErr error
	for _, u := range urls {
		rates, err := p.fetch(ctx, u)
		if err == nil && len(rates) > 0 {
			return rates, nil
		}
		lastErr = err
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("no rates for %s", base)
	}
	return nil, lastErr
}

func (p *HTTPProvider) fetch(ctx context.Context, url string) (map[string]float64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := p.client().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("rates %s: status %d", url, resp.StatusCode)
	}
	var data struct {
		Rates map[string]float64 `json:"rates"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, err
	}
	return data.Rates, nil
}

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
type Overrides map[string]float64

func OverrideKey(base, quote string) string { return base + "/" + quote }

// Rate returns how many `to` one `from` buys.
func (c *Converter) Rate(ctx context.Context, ov Overrides, from, to string) (float64, bool) {
	from, to = strings.ToUpper(strings.TrimSpace(from)), strings.ToUpper(strings.TrimSpace(to))
	if from == to {
		return 1, true
	}
	if r, ok := ov[OverrideKey(from, to)]; ok && r > 0 {
		return r, true
	}
	if r, ok := ov[OverrideKey(to, from)]; ok && r > 0 {
		return 1 / r, true
	}
	if rates := c.providerRates(ctx, from); rates != nil {
		if r, ok := rates[to]; ok && r > 0 {
			return r, true
		}
	}
	if rates := c.providerRates(ctx, to); rates != nil {
		if r, ok := rates[from]; ok && r > 0 {
			return 1 / r, true
		}
	}
	return 0, false
}

// Convert converts a minor-unit amount, rounding half away from zero.
func (c *Converter) Convert(ctx context.Context, ov Overrides, amount int64, from, to string) (int64, bool) {
	r, ok := c.Rate(ctx, ov, from, to)
	if !ok {
		return 0, false
	}
	if r == 1 {
		return amount, true
	}
	return int64(math.Round(float64(amount) * r)), true
}
