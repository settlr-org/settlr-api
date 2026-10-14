package rates

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/settlr-org/settlr-api/internal/httpx"
)

// Source serves rate tables for GET /rates. *HTTPProvider implements it.
type Source interface {
	Rates(ctx context.Context, base, date string) (Result, error)
}

type Handler struct {
	// Source defaults to an HTTPProvider against the public upstreams.
	Source Source
	// Now is the cache clock (tests).
	Now func() time.Time

	mu    sync.Mutex
	cache map[string]cached
}

type cached struct {
	res Result
	ts  time.Time
}

const cacheTTL = time.Hour

func (h *Handler) RegisterRoutes(mux *http.ServeMux, authMw func(http.Handler) http.Handler) {
	mux.Handle("GET /api/v1/rates", authMw(http.HandlerFunc(h.GetRates)))
}

func (h *Handler) source() Source {
	if h.Source != nil {
		return h.Source
	}
	return &HTTPProvider{}
}

func (h *Handler) now() time.Time {
	if h.Now != nil {
		return h.Now()
	}
	return time.Now()
}

// table returns base's rate table for date, cached for an hour.
func (h *Handler) table(ctx context.Context, base, date string) (Result, error) {
	key := base + ":" + date
	h.mu.Lock()
	if h.cache == nil {
		h.cache = make(map[string]cached)
	}
	if c, ok := h.cache[key]; ok && h.now().Sub(c.ts) < cacheTTL {
		h.mu.Unlock()
		return c.res, nil
	}
	h.mu.Unlock()
	res, err := h.source().Rates(ctx, base, date)
	if err != nil {
		return Result{}, err
	}
	h.mu.Lock()
	h.cache[key] = cached{res: res, ts: h.now()}
	h.mu.Unlock()
	return res, nil
}

// complete fills currencies missing from base's table with cross rates through
// each pivot quoted in it: base→quote = base→pivot × pivot→quote. A missing
// pivot table is not an error; the table is then returned as is.
func (h *Handler) complete(ctx context.Context, res Result, date string) Result {
	out := make(map[string]float64, len(res.Rates))
	for k, v := range res.Rates {
		out[k] = v
	}
	crossed := false
	for _, p := range Pivots {
		bp, ok := out[p]
		if p == res.Base || !ok || bp <= 0 {
			continue
		}
		pt, err := h.table(ctx, p, date)
		if err != nil {
			continue
		}
		for q, r := range pt.Rates {
			if _, have := out[q]; have || q == res.Base || r <= 0 {
				continue
			}
			out[q] = bp * r
			crossed = true
		}
	}
	res.Rates = out
	if crossed {
		res.Source += "+cross"
	}
	return res
}

// GetRates returns 1 base = rates[quote] quote for every currency known to the
// upstream providers. Gaps in one provider's table are filled from the other and
// then by crossing through EUR/USD, so ?base=USD and ?base=NPR quote each other.
func (h *Handler) GetRates(w http.ResponseWriter, r *http.Request) {
	base := strings.ToUpper(strings.TrimSpace(r.URL.Query().Get("base")))
	if base == "" {
		base = "NPR"
	}
	date := strings.TrimSpace(r.URL.Query().Get("date")) // YYYY-MM-DD optional, for Frankfurter historical
	ctx := r.Context()
	res, err := h.table(ctx, base, date)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	res = h.complete(ctx, res, date)
	resp := map[string]any{"base": res.Base, "rates": res.Rates, "source": res.Source}
	if res.Date != "" {
		resp["date"] = res.Date
	} else {
		resp["date"] = date
	}
	httpx.WriteJSON(w, http.StatusOK, resp)
}
