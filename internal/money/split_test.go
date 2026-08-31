package money

import "testing"

func TestSplitEqual(t *testing.T) {
	tests := []struct {
		name   string
		amount int64
		n      int
		want   []int64
	}{
		{"100/3", 100, 3, []int64{34, 33, 33}},
		{"100/4", 100, 4, []int64{25, 25, 25, 25}},
		{"10/3", 10, 3, []int64{4, 3, 3}},
		{"1/2", 1, 2, []int64{1, 0}},
		{"10000/3 like $100 equal 3", 10000, 3, []int64{3334, 3333, 3333}},
		{"1 cent single", 1, 1, []int64{1}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := SplitEqual(tc.amount, tc.n)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("len: got %d want %d", len(got), len(tc.want))
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("idx %d: got %d want %d", i, got[i], tc.want[i])
				}
			}
			var sum int64
			for _, v := range got {
				sum += v
			}
			if sum != tc.amount {
				t.Fatalf("sum %d != amount %d", sum, tc.amount)
			}
		})
	}
}

func TestSplitEqualErrors(t *testing.T) {
	if _, err := SplitEqual(100, 0); err == nil {
		t.Fatal("expected error for n=0")
	}
	if _, err := SplitEqual(0, 3); err == nil {
		t.Fatal("expected error for amount=0")
	}
}

func TestSplitExact(t *testing.T) {
	if _, err := SplitExact(100, []int64{30, 30, 40}); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if _, err := SplitExact(100, []int64{30, 30, 30}); err == nil {
		t.Fatal("expected error for sum mismatch")
	}
	if _, err := SplitExact(100, []int64{50, -10, 60}); err == nil {
		t.Fatal("expected error for negative")
	}
}

func TestSplitByPercentage(t *testing.T) {
	// 25/25/50 split of $100 (10000 cents)
	got, err := SplitByPercentage(10000, []int64{2500, 2500, 5000})
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if got[0] != 2500 || got[1] != 2500 || got[2] != 5000 {
		t.Fatalf("got %v", got)
	}

	// Rounding case: 33.33/33.33/33.34 of 100 cents -> 33,33,34
	got, err = SplitByPercentage(100, []int64{3333, 3333, 3334})
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	var sum int64
	for _, v := range got {
		sum += v
	}
	if sum != 100 {
		t.Fatalf("sum %d", sum)
	}

	// Must sum to 10000
	if _, err := SplitByPercentage(100, []int64{5000, 4000}); err == nil {
		t.Fatal("expected error for not summing to 10000")
	}
}

func TestSplitByShares(t *testing.T) {
	// 1:2:3 of 600 -> 100,200,300
	got, err := SplitByShares(600, []int64{1, 2, 3})
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if got[0] != 100 || got[1] != 200 || got[2] != 300 {
		t.Fatalf("got %v", got)
	}
	// Rounding: 100 / shares 1,1,1 -> 34,33,33 (first by tie-break)
	got, err = SplitByShares(100, []int64{1, 1, 1})
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	var sum int64
	for _, v := range got {
		sum += v
	}
	if sum != 100 {
		t.Fatalf("sum %d", sum)
	}
	if got[0] != 34 || got[1] != 33 || got[2] != 33 {
		t.Fatalf("got %v", got)
	}

	if _, err := SplitByShares(100, []int64{1, 0, 1}); err == nil {
		t.Fatal("expected error for zero share")
	}
}

func TestInvariant_SumEqualsAmount(t *testing.T) {
	cases := []struct {
		amount int64
		n      int
	}{
		{1, 3}, {2, 3}, {99, 7}, {10001, 5}, {100000, 6},
	}
	for _, c := range cases {
		got, _ := SplitEqual(c.amount, c.n)
		var sum int64
		for _, v := range got {
			sum += v
		}
		if sum != c.amount {
			t.Fatalf("equal: amount %d n %d sum %d", c.amount, c.n, sum)
		}
	}
	// shares
	got2, _ := SplitByShares(1000, []int64{2, 3, 7, 1})
	var s2 int64
	for _, v := range got2 {
		s2 += v
	}
	if s2 != 1000 {
		t.Fatalf("shares sum %d", s2)
	}
	got3, _ := SplitByPercentage(1000, []int64{1000, 2000, 3000, 4000})
	var s3 int64
	for _, v := range got3 {
		s3 += v
	}
	if s3 != 1000 {
		t.Fatalf("pct sum %d", s3)
	}
}
