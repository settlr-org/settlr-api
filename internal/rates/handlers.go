package rates

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/nabinkhanal00/settlr-api/internal/httpx"
)

type Handler struct{}

var cache = struct {
	sync.RWMutex
	data map[string]cached
}{data: make(map[string]cached)}

type cached struct {
	rates map[string]float64
	ts    time.Time
}

func (h *Handler) RegisterRoutes(mux *http.ServeMux, authMw func(http.Handler) http.Handler) {
	mux.Handle("GET /api/v1/rates", authMw(http.HandlerFunc(h.GetRates)))
}

func (h *Handler) GetRates(w http.ResponseWriter, r *http.Request) {
	base := strings.ToUpper(strings.TrimSpace(r.URL.Query().Get("base")))
	if base == "" {
		base = "NPR"
	}
	date := strings.TrimSpace(r.URL.Query().Get("date")) // YYYY-MM-DD optional, for Frankfurter historical
	// check cache 1h (key includes date)
	cacheKey := base
	if date != "" {
		cacheKey = base + ":" + date
	}
	cache.RLock()
	if c, ok := cache.data[cacheKey]; ok && time.Since(c.ts) < time.Hour {
		cache.RUnlock()
		httpx.WriteJSON(w, 200, map[string]any{"base": base, "date": date, "rates": c.rates, "cached": true})
		return
	}
	cache.RUnlock()
	// Try Frankfurter first (supports 34 codes, no key, handles historical)
	frankfurterURL := "https://api.frankfurter.app/latest?from=" + base
	if date != "" {
		frankfurterURL = "https://api.frankfurter.app/" + date + "?from=" + base
	}
	resp, err := http.Get(frankfurterURL)
	if err == nil && resp.StatusCode == 200 {
		defer resp.Body.Close()
		var fdata struct {
			Rates map[string]float64 `json:"rates"`
			Base  string             `json:"base"`
			Date  string             `json:"date"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&fdata); err == nil && len(fdata.Rates) > 0 {
			cache.Lock()
			cache.data[cacheKey] = cached{rates: fdata.Rates, ts: time.Now()}
			cache.Unlock()
			httpx.WriteJSON(w, 200, map[string]any{"base": fdata.Base, "date": fdata.Date, "rates": fdata.Rates, "source": "frankfurter"})
			return
		}
		if resp != nil {
			resp.Body.Close()
		}
	}
	// Fallback to exchangerate-api
	resp, err = http.Get("https://api.exchangerate-api.com/v4/latest/" + base)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	defer resp.Body.Close()
	var data struct {
		Rates map[string]float64 `json:"rates"`
		Base  string             `json:"base"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	cache.Lock()
	cache.data[cacheKey] = cached{rates: data.Rates, ts: time.Now()}
	cache.Unlock()
	httpx.WriteJSON(w, 200, map[string]any{"base": data.Base, "rates": data.Rates, "source": "exchangerate-api"})
}
