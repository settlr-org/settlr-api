package debts

import (
	"testing"

	"github.com/google/uuid"
)

func u(seed string) uuid.UUID {
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte(seed))
}

func TestSimplify_Simple(t *testing.T) {
	// Alice +100, Bob -60, Charlie -40
	bals := []Balance{
		{u("alice"), 10000},
		{u("bob"), -6000},
		{u("charlie"), -4000},
	}
	debts := Simplify(bals)
	if len(debts) != 2 {
		t.Fatalf("got %d debts, want 2: %v", len(debts), debts)
	}
	// Conservation
	var pos int64
	for _, b := range bals {
		if b.Amount > 0 {
			pos += b.Amount
		}
	}
	var totalDebt int64
	for _, d := range debts {
		totalDebt += d.Amount
	}
	if totalDebt != pos {
		t.Fatalf("totalDebt %d != pos %d", totalDebt, pos)
	}
	// Verify each debt amount conserves participant clearing
}

func TestSimplify_FourWay(t *testing.T) {
	bals := []Balance{
		{u("a"), 10000},
		{u("b"), 5000},
		{u("c"), -8000},
		{u("d"), -7000},
	}
	debts := Simplify(bals)
	if len(debts) > 3 {
		t.Fatalf("expected <=3 debts, got %d", len(debts))
	}
	var sum int64
	for _, d := range debts {
		sum += d.Amount
	}
	if sum != 15000 {
		t.Fatalf("sum %d want 15000", sum)
	}
	// Simulate settlement to verify balances clear
	m := map[uuid.UUID]int64{}
	for _, b := range bals {
		m[b.UserID] = b.Amount
	}
	for _, d := range debts {
		m[d.From] += d.Amount
		m[d.To] -= d.Amount
	}
	for id, v := range m {
		if v != 0 {
			t.Fatalf("participant %s not settled: %d", id, v)
		}
	}
}

func TestSimplify_ZeroBalances(t *testing.T) {
	debts := Simplify([]Balance{{u("a"), 0}, {u("b"), 0}})
	if len(debts) != 0 {
		t.Fatalf("expected 0 debts, got %v", debts)
	}
}

func TestSimplify_AlreadySettled(t *testing.T) {
	debts := Simplify(nil)
	if len(debts) != 0 {
		t.Fatalf("nil input: %v", debts)
	}
}

func TestSimplify_ConservesMoney(t *testing.T) {
	cases := [][]Balance{
		{{u("a"), 3000}, {u("b"), 2000}, {u("c"), -5000}},
		{{u("a"), 100}, {u("b"), -50}, {u("c"), -50}},
		{{u("a"), 1}, {u("b"), -1}},
	}
	for i, bals := range cases {
		debts := Simplify(bals)
		var pos int64
		for _, b := range bals {
			if b.Amount > 0 {
				pos += b.Amount
			}
		}
		var td int64
		for _, d := range debts {
			td += d.Amount
		}
		if pos != td {
			t.Fatalf("case %d pos %d != td %d", i, pos, td)
		}
		// Check all cleared
		m := map[uuid.UUID]int64{}
		for _, b := range bals {
			m[b.UserID] = b.Amount
		}
		for _, d := range debts {
			m[d.From] += d.Amount
			m[d.To] -= d.Amount
		}
		for _, v := range m {
			if v != 0 {
				t.Fatalf("case %d not cleared: %v", i, m)
			}
		}
	}
}

func TestSimplify_Deterministic(t *testing.T) {
	bals := []Balance{{u("b"), 5000}, {u("a"), 5000}, {u("c"), -10000}}
	d1 := Simplify(bals)
	d2 := Simplify(bals)
	if len(d1) != len(d2) {
		t.Fatal("non-deterministic length")
	}
	for i := range d1 {
		if d1[i] != d2[i] {
			t.Fatalf("non-deterministic at %d: %v vs %v", i, d1[i], d2[i])
		}
	}
}
