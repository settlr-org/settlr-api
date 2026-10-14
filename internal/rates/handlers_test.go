package rates

import (
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// upstreams fakes Frankfurter (ECB set: no NPR, rejects base NPR) and
// exchangerate-api (has NPR) on a local server; nothing touches the network.
func upstreams(t *testing.T) (*HTTPProvider, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	frank := map[string]map[string]float64{
		"USD": {"EUR": 0.8, "GBP": 0.68, "JPY": 150},
		"EUR": {"USD": 1.25, "GBP": 0.85, "JPY": 187.5},
	}
	fallback := map[string]map[string]float64{
		"USD": {"USD": 1, "EUR": 0.81, "NPR": 133, "KWD": 0.3},
		"NPR": {"NPR": 1, "USD": 0.0075, "EUR": 0.006},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		var table map[string]float64
		switch {
		case strings.HasPrefix(r.URL.Path, "/frank/"):
			table = frank[r.URL.Query().Get("from")]
		case strings.HasPrefix(r.URL.Path, "/fallback/"):
			table = fallback[strings.TrimPrefix(r.URL.Path, "/fallback/")]
		}
		if table == nil {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"rates": table, "date": "2026-10-13"})
	}))
	t.Cleanup(srv.Close)
	return &HTTPProvider{Client: srv.Client(), FrankfurterURL: srv.URL + "/frank", FallbackURL: srv.URL + "/fallback"}, &hits
}

func getRates(t *testing.T, h *Handler, base string) map[string]float64 {
	t.Helper()
	rec := httptest.NewRecorder()
	h.GetRates(rec, httptest.NewRequest(http.MethodGet, "/api/v1/rates?base="+base, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("base %s: status %d %s", base, rec.Code, rec.Body)
	}
	var body struct {
		Base  string             `json:"base"`
		Rates map[string]float64 `json:"rates"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body.Base != base {
		t.Fatalf("base %s: %v %s", base, err, rec.Body)
	}
	return body.Rates
}

func TestHTTPProviderMergesFallback(t *testing.T) {
	p, _ := upstreams(t)
	usd, err := p.Latest(t.Context(), "USD")
	if err != nil {
		t.Fatal(err)
	}
	if usd["NPR"] != 133 || usd["KWD"] != 0.3 {
		t.Fatalf("fallback should fill currencies Frankfurter lacks: %v", usd)
	}
	if usd["EUR"] != 0.8 {
		t.Fatalf("Frankfurter should win where both quote: %v", usd)
	}
	if _, ok := usd["USD"]; ok {
		t.Fatal("base should not quote itself")
	}
	npr, err := p.Latest(t.Context(), "npr")
	if err != nil || npr["USD"] != 0.0075 {
		t.Fatalf("NPR via fallback: %v %v", npr, err)
	}

	// The converter on top of the real provider resolves NPR both ways.
	c := NewConverter(p)
	if v, ok := c.Convert(t.Context(), nil, 100, "USD", "NPR"); !ok || v != 13300 {
		t.Fatalf("USD→NPR: %d %v", v, ok)
	}
	if v, ok := c.Convert(t.Context(), nil, 100, "NPR", "GBP"); !ok || v != 1 { // NPR 1 → £0.0051
		t.Fatalf("NPR→GBP: %d %v", v, ok)
	}
}

func TestGetRatesSymmetricAndCrossed(t *testing.T) {
	p, hits := upstreams(t)
	h := &Handler{Source: p}
	usd := getRates(t, h, "USD")
	npr := getRates(t, h, "NPR")
	if usd["NPR"] == 0 || npr["USD"] == 0 {
		t.Fatalf("USD and NPR should quote each other: USD=%v NPR=%v", usd, npr)
	}
	// NPR's own table has no GBP/JPY; they are crossed through EUR.
	if math.Abs(npr["GBP"]-0.006*0.85) > 1e-12 || math.Abs(npr["JPY"]-0.006*187.5) > 1e-12 {
		t.Fatalf("cross rates via EUR: %v", npr)
	}
	n := hits.Load()
	getRates(t, h, "USD")
	if hits.Load() != n {
		t.Fatal("tables should be cached")
	}
}
