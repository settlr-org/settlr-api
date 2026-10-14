package wallet

import "time"

// Interval units of a recurring planned payment.
const (
	unitDay   = "DAY"
	unitWeek  = "WEEK"
	unitMonth = "MONTH"
	unitYear  = "YEAR"
)

var intervalUnits = map[string]bool{unitDay: true, unitWeek: true, unitMonth: true, unitYear: true}

// addMonthsClamped adds n months to t, clamping the day to the end of the target
// month (Jan 31 + 1 month = Feb 28/29). The time of day is kept. All arithmetic
// is on the UTC calendar.
func addMonthsClamped(t time.Time, n int) time.Time {
	t = t.UTC()
	y, m, d := t.Date()
	first := time.Date(y, m+time.Month(n), 1, t.Hour(), t.Minute(), t.Second(), t.Nanosecond(), time.UTC)
	last := first.AddDate(0, 1, -1).Day()
	if d > last {
		d = last
	}
	return first.AddDate(0, 0, d-1)
}

// occurrence returns the k-th (0-based) occurrence of a schedule starting at
// start, every n units. Occurrences are always computed from the anchor, so a
// plan starting on the 31st returns to the 31st after a short month.
func occurrence(start time.Time, n int, unit string, k int) time.Time {
	start = start.UTC()
	switch unit {
	case unitDay:
		return start.AddDate(0, 0, k*n)
	case unitWeek:
		return start.AddDate(0, 0, 7*k*n)
	case unitYear:
		return addMonthsClamped(start, 12*k*n)
	default:
		return addMonthsClamped(start, k*n)
	}
}

// nextOccurrenceAfter returns the first occurrence strictly after `after`.
func nextOccurrenceAfter(start time.Time, n int, unit string, after time.Time) time.Time {
	start, after = start.UTC(), after.UTC()
	if n < 1 {
		n = 1
	}
	if start.After(after) {
		return start
	}
	// Estimate k from the elapsed calendar span, then step forward.
	var k int
	switch unit {
	case unitDay, unitWeek:
		days := int(after.Sub(start).Hours() / 24)
		step := n
		if unit == unitWeek {
			step = 7 * n
		}
		k = days/step - 1
	default:
		months := (after.Year()-start.Year())*12 + int(after.Month()-start.Month())
		step := n
		if unit == unitYear {
			step = 12 * n
		}
		k = months/step - 1
	}
	if k < 0 {
		k = 0
	}
	for !occurrence(start, n, unit, k).After(after) {
		k++
	}
	return occurrence(start, n, unit, k)
}
