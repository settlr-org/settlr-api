package rates

import (
	"context"
	"errors"
	"testing"
	"time"
)

type countingProvider struct {
	calls int
	rates map[string]map[string]float64
	err   error
}

func (p *countingProvider) Latest(_ context.Context, base string) (map[string]float64, error) {
	p.calls++
	if p.err != nil {
		return nil, p.err
	}
	return p.rates[base], nil
}

func TestConverter(t *testing.T) {
	ctx := context.Background()
	p := &countingProvider{rates: map[string]map[string]float64{"USD": {"NPR": 133}}}
	now := time.Date(2026, 10, 13, 10, 0, 0, 0, time.UTC)
	c := &Converter{Provider: p, Now: func() time.Time { return now }}

	if v, ok := c.Convert(ctx, nil, 250, "USD", "NPR"); !ok || v != 33250 {
		t.Fatalf("provider rate: %d %v", v, ok)
	}
	if v, ok := c.Convert(ctx, nil, 13300, "NPR", "USD"); !ok || v != 100 {
		t.Fatalf("inverse provider rate: %d %v", v, ok)
	}
	calls := p.calls
	c.Convert(ctx, nil, 1, "USD", "NPR")
	if p.calls != calls {
		t.Fatal("rates should be cached for the day")
	}
	now = now.Add(24 * time.Hour)
	c.Convert(ctx, nil, 1, "USD", "NPR")
	if p.calls == calls {
		t.Fatal("cache should expire on a new day")
	}

	ov := Overrides{OverrideKey("USD", "NPR"): 140}
	if v, _ := c.Convert(ctx, ov, 100, "USD", "NPR"); v != 14000 {
		t.Fatalf("override: %d", v)
	}
	if v, _ := c.Convert(ctx, ov, 14000, "NPR", "USD"); v != 100 {
		t.Fatalf("inverse override: %d", v)
	}
	if v, ok := c.Convert(ctx, nil, 7, "EUR", "EUR"); !ok || v != 7 {
		t.Fatal("identity")
	}
	if _, ok := c.Convert(ctx, nil, 7, "EUR", "JPY"); ok {
		t.Fatal("unknown pair should fail")
	}

	var nilConv *Converter
	if v, ok := nilConv.Convert(ctx, ov, 1, "USD", "NPR"); !ok || v != 140 {
		t.Fatal("nil converter should still apply overrides")
	}

	failing := &countingProvider{err: errors.New("down")}
	fc := &Converter{Provider: failing, Now: func() time.Time { return now }}
	fc.Convert(ctx, nil, 1, "USD", "NPR")
	n := failing.calls
	fc.Convert(ctx, nil, 1, "USD", "NPR")
	if failing.calls != n {
		t.Fatal("failed fetches should be cached briefly")
	}
}
