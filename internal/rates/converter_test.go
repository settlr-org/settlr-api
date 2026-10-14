package rates

import (
	"context"
	"errors"
	"math"
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

func TestRateInverseAndCross(t *testing.T) {
	ctx := context.Background()
	// Mirrors the upstream asymmetry: the USD table (Frankfurter/ECB) lacks NPR,
	// the NPR table (exchangerate-api) has USD; GBP is only quoted against EUR.
	p := &countingProvider{rates: map[string]map[string]float64{
		"USD": {"EUR": 0.8, "JPY": 150},
		"NPR": {"USD": 0.0075, "EUR": 0.006},
		"EUR": {"USD": 1.25, "GBP": 0.85, "JPY": 187.5},
	}}
	c := NewConverter(p)
	approx := func(name string, got float64, ok bool, want float64) {
		t.Helper()
		if !ok || math.Abs(got-want) > 1e-9*math.Max(1, want) {
			t.Fatalf("%s: got %v %v, want %v", name, got, ok, want)
		}
	}
	r, ok := c.Rate(ctx, nil, "USD", "NPR")
	approx("USD/NPR via inverse of NPR/USD", r, ok, 1/0.0075)
	r, ok = c.Rate(ctx, nil, "NPR", "GBP")
	approx("NPR/GBP via EUR pivot", r, ok, 0.006*0.85)
	r, ok = c.Rate(ctx, nil, "GBP", "NPR")
	approx("GBP/NPR via inverse legs", r, ok, 1/(0.006*0.85))
	r, ok = c.Rate(ctx, nil, "GBP", "USD")
	approx("GBP/USD via EUR", r, ok, 1.25/0.85)
	if _, ok := c.Rate(ctx, nil, "NPR", "XYZ"); ok {
		t.Fatal("unknown currency should not resolve")
	}

	// Overrides beat provider and cross rates, in either direction.
	ov := Overrides{OverrideKey("npr", "usd"): 0.01}
	r, ok = c.Rate(ctx, ov, "USD", "NPR")
	approx("override inverted", r, ok, 100)
	r, ok = c.Rate(ctx, ov, "NPR", "USD")
	approx("override direct", r, ok, 0.01)
	// Overrides also serve as cross legs: NPR→USD→JPY when nothing else knows NPR.
	only := NewConverter(&countingProvider{rates: map[string]map[string]float64{"USD": {"JPY": 150}}})
	r, ok = only.Rate(ctx, Overrides{OverrideKey("USD", "NPR"): 125}, "NPR", "JPY")
	approx("override leg via USD pivot", r, ok, 150.0/125)
}

func TestConvertUsesTargetPrecision(t *testing.T) {
	ctx := context.Background()
	p := &countingProvider{rates: map[string]map[string]float64{
		"USD": {"JPY": 150, "KWD": 0.3075, "NPR": 133.33},
	}}
	c := NewConverter(p)
	cases := []struct {
		amount   int64
		from, to string
		want     int64
	}{
		{333, "USD", "JPY", 50000},     // $3.33 → ¥499.5 → ¥500 (whole yen, stored ×100)
		{100000, "JPY", "USD", 667},    // ¥1000 → $6.666… → $6.67
		{1000, "USD", "KWD", 308},      // $10 → KWD 3.075 → 3.08 at storage scale
		{-333, "USD", "JPY", -50000},   // half away from zero
		{150, "USD", "NPR", 20000},     // $1.50 → NPR 199.995 → 200.00
		{123456, "JPY", "JPY", 123456}, // identity untouched
	}
	for _, tc := range cases {
		if v, ok := c.Convert(ctx, nil, tc.amount, tc.from, tc.to); !ok || v != tc.want {
			t.Errorf("Convert(%d %s→%s) = %d %v, want %d", tc.amount, tc.from, tc.to, v, ok, tc.want)
		}
	}
}
