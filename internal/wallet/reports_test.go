package wallet

import (
	"math/rand"
	"strings"
	"testing"
	"time"
)

func TestPercentTenthsSumsToExactly100(t *testing.T) {
	cases := [][]int64{
		{1, 1, 1},
		{2, 1},
		{21150, 5000},
		{1},
		{0, 0},
		{},
		{9007199254740993, 9007199254740993, 1},
		{3333, 3333, 3334},
	}
	rng := rand.New(rand.NewSource(7))
	for i := 0; i < 500; i++ {
		n := 1 + rng.Intn(20)
		c := make([]int64, n)
		for j := range c {
			c[j] = int64(1 + rng.Intn(100000))
		}
		cases = append(cases, c)
	}
	for _, c := range cases {
		got := percentTenths(c)
		var sum, total int64
		for _, a := range c {
			total += a
		}
		for i, v := range got {
			sum += v
			if v < 0 || (c[i] == 0 && v != 0) {
				t.Fatalf("%v -> %v", c, got)
			}
		}
		want := int64(1000)
		if total == 0 {
			want = 0
		}
		if sum != want {
			t.Fatalf("%v -> %v sums to %d, want %d", c, got, sum, want)
		}
	}
	if got := percentTenths([]int64{1, 1, 1}); got[0] != 334 || got[1] != 333 || got[2] != 333 {
		t.Fatalf("tie-break favours the earlier (larger) item: %v", got)
	}
}

func TestBucketBoundaries(t *testing.T) {
	d := func(s string) time.Time { return day(s) }
	cases := []struct {
		in       string
		bucket   string
		startDay int
		want     string
	}{
		{"2026-05-03", "week", 1, "2026-04-27"}, // Sunday -> previous Monday
		{"2026-05-04", "week", 1, "2026-05-04"}, // Monday
		{"2026-12-31", "week", 1, "2026-12-28"},
		{"2026-01-01", "week", 1, "2025-12-29"}, // across the year
		{"2026-02-28", "month", 1, "2026-02-01"},
		{"2024-02-29", "month", 1, "2024-02-01"},
		{"2026-05-14", "month", 15, "2026-04-15"},
		{"2026-05-15", "month", 15, "2026-05-15"},
		{"2026-01-10", "month", 15, "2025-12-15"},
		{"2026-03-31", "month", 28, "2026-03-28"},
		{"2026-03-27", "month", 28, "2026-02-28"},
	}
	for _, c := range cases {
		if got := bucketStart(d(c.in), c.bucket, c.startDay); !got.Equal(d(c.want)) {
			t.Errorf("bucketStart(%s,%s,%d) = %s, want %s", c.in, c.bucket, c.startDay, got.Format("2006-01-02"), c.want)
		}
	}
	// Months starting on the 28th never clamp: Jan 28 -> Feb 28 -> Mar 28.
	s := d("2026-01-28")
	s = nextBucket(s, "month")
	s = nextBucket(s, "month")
	if !s.Equal(d("2026-03-28")) {
		t.Fatalf("month stepping: %s", s)
	}
	// A late timestamp in the evening belongs to its UTC day (no DST, no local time).
	late := time.Date(2026, 3, 29, 23, 59, 59, 0, time.UTC)
	if got := bucketStart(late, "day", 1); !got.Equal(d("2026-03-29")) {
		t.Fatalf("day bucket: %s", got)
	}
}

func TestSearchTerms(t *testing.T) {
	got := searchTerms("  50%  off_x \\ ")
	if len(got) != 3 || got[0] != `%50\%%` || got[1] != `%off\_x%` || got[2] != `%\\%` {
		t.Fatalf("%q", got)
	}
	if got := searchTerms("   "); len(got) != 0 {
		t.Fatalf("blank: %v", got)
	}
	// Too many / too long: truncated, not rejected.
	if got := searchTerms("a b c d e f g h i j k l"); len(got) != 10 || got[9] != "%j%" {
		t.Fatalf("terms: %v", got)
	}
	long := strings.Repeat("é", 150)
	if got := searchTerms(long); len([]rune(got[0])) != 102 {
		t.Fatalf("term length: %d", len([]rune(got[0])))
	}
}
