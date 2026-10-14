package money

import "testing"

func TestExponent(t *testing.T) {
	for code, want := range map[string]int{"USD": 2, "NPR": 2, "jpy": 0, "KRW": 0, "VND": 0, "KWD": 3, "BHD": 3, "CLF": 4, "ZZZ": 2} {
		if got := Exponent(code); got != want {
			t.Errorf("Exponent(%s) = %d, want %d", code, got, want)
		}
	}
	if Precision("KWD") != StorageScale || Precision("JPY") != 0 || Precision("EUR") != 2 {
		t.Fatal("precision is the ISO exponent capped at the storage scale")
	}
}

func TestConvertMinor(t *testing.T) {
	cases := []struct {
		amount   int64
		rate     float64
		from, to string
		want     int64
	}{
		{250, 133, "USD", "NPR", 33250},
		{10000, 0.1092, "KRW", "JPY", 1100}, // ₩100 → ¥10.92 → ¥11 (stored ×100)
		{149, 1, "USD", "JPY", 100},         // $1.49 at par → ¥1
		{150, 1, "USD", "JPY", 200},         // half rounds up to ¥2
		{-150, 1, "USD", "JPY", -200},       // and away from zero
		{100000, 0.0067, "JPY", "USD", 670},
		{1000, 3.2549, "KWD", "USD", 3255}, // KWD 10.00 → $32.549 → $32.55
		{29, 1, "EUR", "USD", 29},
	}
	for _, tc := range cases {
		if got := ConvertMinor(tc.amount, tc.rate, tc.from, tc.to); got != tc.want {
			t.Errorf("ConvertMinor(%d, %v, %s→%s) = %d, want %d", tc.amount, tc.rate, tc.from, tc.to, got, tc.want)
		}
	}
}
