package money

import (
	"math/rand"
	"testing"
)

func TestProperty_SplitEqual_Random(t *testing.T) {
	for iter := 0; iter < 1000; iter++ {
		amount := int64(rand.Intn(100000) + 1)
		n := rand.Intn(10) + 1
		splits, err := SplitEqual(amount, n)
		if err != nil {
			t.Fatalf("iter %d: unexpected err %v", iter, err)
		}
		var sum int64
		for _, v := range splits {
			sum += v
		}
		if sum != amount {
			t.Fatalf("iter %d: sum %d != amount %d n=%d splits %v", iter, sum, amount, n, splits)
		}
		// deterministic: second call same
		splits2, _ := SplitEqual(amount, n)
		for i := range splits {
			if splits[i] != splits2[i] {
				t.Fatalf("non-deterministic")
			}
		}
	}
}

func TestProperty_SplitByShares_Random(t *testing.T) {
	for iter := 0; iter < 1000; iter++ {
		amount := int64(rand.Intn(100000) + 1)
		n := rand.Intn(6) + 1
		shares := make([]int64, n)
		for i := range shares {
			shares[i] = int64(rand.Intn(10) + 1)
		}
		splits, err := SplitByShares(amount, shares)
		if err != nil {
			t.Fatalf("shares err %v", err)
		}
		var sum int64
		for _, v := range splits {
			sum += v
		}
		if sum != amount {
			t.Fatalf("shares sum %d != %d shares %v", sum, amount, shares)
		}
	}
}

func TestProperty_SplitByPercentage_Random(t *testing.T) {
	for iter := 0; iter < 1000; iter++ {
		amount := int64(rand.Intn(100000) + 1)
		n := rand.Intn(5) + 2
		// generate random percentages that sum to 10000
		cuts := make([]int, n-1)
		for i := range cuts {
			cuts[i] = rand.Intn(10001)
		}
		// simple: distribute 10000 randomly
		bps := make([]int64, n)
		remaining := int64(10000)
		for i := 0; i < n-1; i++ {
			v := int64(rand.Intn(int(remaining+1)))
			bps[i] = v
			remaining -= v
		}
		bps[n-1] = remaining
		// shuffle bps
		rand.Shuffle(n, func(a, b int) { bps[a], bps[b] = bps[b], bps[a] })
		splits, err := SplitByPercentage(amount, bps)
		if err != nil {
			t.Fatalf("pct err %v bps %v", err, bps)
		}
		var sum int64
		for _, v := range splits {
			sum += v
		}
		if sum != amount {
			t.Fatalf("pct sum %d != %d bps %v splits %v", sum, amount, bps, splits)
		}
	}
}

func TestProperty_SplitExact_RoundTrip(t *testing.T) {
	amount := int64(10000)
	shares := []int64{34, 33, 33}
	// via equal
	splits, _ := SplitEqual(amount, 3)
	if got, err := SplitExact(amount, splits); err != nil || len(got) != 3 {
		t.Fatalf("exact roundtrip failed %v", err)
	}
	_ = shares
}
