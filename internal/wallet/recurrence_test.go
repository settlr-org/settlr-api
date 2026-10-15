package wallet

import (
	"testing"
	"time"
)

func day(s string) time.Time {
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		panic(err)
	}
	return t
}

func TestAddMonthsClamped(t *testing.T) {
	cases := []struct {
		start string
		n     int
		want  string
	}{
		{"2026-01-31", 1, "2026-02-28"},
		{"2028-01-31", 1, "2028-02-29"},
		{"2026-01-31", 2, "2026-03-31"},
		{"2026-03-31", 1, "2026-04-30"},
		{"2026-12-15", 1, "2027-01-15"},
		{"2028-02-29", 12, "2029-02-28"},
		{"2028-02-29", 48, "2032-02-29"},
	}
	for _, c := range cases {
		if got := addMonthsClamped(day(c.start), c.n); !got.Equal(day(c.want)) {
			t.Errorf("%s + %d months = %s, want %s", c.start, c.n, got.Format(time.DateOnly), c.want)
		}
	}
}

func TestNextOccurrenceAfter(t *testing.T) {
	at := func(s string) time.Time { return day(s).Add(9 * time.Hour) }
	cases := []struct {
		start, after string
		n            int
		unit         string
		want         string
	}{
		// Before the start: the start itself.
		{"2026-05-10", "2026-01-01", 1, unitMonth, "2026-05-10"},
		{"2026-01-01", "2026-01-01", 1, unitDay, "2026-01-02"},
		{"2026-01-01", "2026-01-10", 3, unitDay, "2026-01-13"},
		{"2026-01-01", "2026-01-08", 1, unitWeek, "2026-01-15"},
		{"2026-01-01", "2026-01-07", 2, unitWeek, "2026-01-15"},
		// Month-end clamp, anchored to the start: back to the 31st after February.
		{"2026-01-31", "2026-01-31", 1, unitMonth, "2026-02-28"},
		{"2026-01-31", "2026-02-28", 1, unitMonth, "2026-03-31"},
		{"2026-01-31", "2026-03-31", 1, unitMonth, "2026-04-30"},
		{"2026-01-31", "2026-01-31", 3, unitMonth, "2026-04-30"},
		// Leap day yearly: Feb 28 in common years, Feb 29 again in leap years.
		{"2028-02-29", "2028-02-29", 1, unitYear, "2029-02-28"},
		{"2028-02-29", "2031-02-28", 1, unitYear, "2032-02-29"},
		// Far behind: the estimate still lands on the first later occurrence.
		{"2020-01-15", "2026-06-20", 1, unitMonth, "2026-07-15"},
		{"2020-01-01", "2026-06-20", 10, unitDay, "2026-06-28"},
	}
	for _, c := range cases {
		got := nextOccurrenceAfter(at(c.start), c.n, c.unit, at(c.after))
		if !got.Equal(at(c.want)) {
			t.Errorf("%s every %d %s after %s = %s, want %s", c.start, c.n, c.unit, c.after, got, c.want)
		}
	}
}
