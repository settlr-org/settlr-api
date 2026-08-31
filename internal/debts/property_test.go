package debts

import (
	"math/rand"
	"testing"

	"github.com/google/uuid"
)

func TestProperty_Simplify_ConservesAndClears(t *testing.T) {
	for iter := 0; iter < 500; iter++ {
		n := rand.Intn(6) + 2 // 2..7 participants
		// generate random balances that sum to 0
		bals := make([]Balance, n)
		var sum int64
		for i := 0; i < n-1; i++ {
			// random amount -5000..5000
			amt := int64(rand.Intn(10001) - 5000)
			bals[i] = Balance{UserID: uuid.New(), Amount: amt}
			sum += amt
		}
		bals[n-1] = Balance{UserID: uuid.New(), Amount: -sum}
		// Now sum is 0
		debts := Simplify(bals)
		// Check conserve: sum of positive == sum of debts
		var pos int64
		for _, b := range bals {
			if b.Amount > 0 {
				pos += b.Amount
			}
		}
		var debtSum int64
		for _, d := range debts {
			if d.Amount <= 0 {
				t.Fatalf("debt amount <=0 %v", d)
			}
			debtSum += d.Amount
		}
		if pos != debtSum {
			t.Fatalf("iter %d pos %d != debtSum %d bals %v debts %v", iter, pos, debtSum, bals, debts)
		}
		// Recompute balances from debts: applying debts should clear original balances
		m := make(map[uuid.UUID]int64)
		for _, b := range bals {
			m[b.UserID] = b.Amount
		}
		for _, d := range debts {
			m[d.From] += d.Amount
			m[d.To] -= d.Amount
		}
		for id, v := range m {
			if v != 0 {
				t.Fatalf("iter %d not cleared id %s val %d m %v debts %v", iter, id, v, m, debts)
			}
		}
		// Edges <= n-1
		if len(debts) > n-1 && n > 1 {
			t.Fatalf("too many debts %d > %d", len(debts), n-1)
		}
	}
}

func TestProperty_Simplify_ZeroSum(t *testing.T) {
	// Single user zero balance
	bals := []Balance{{UserID: uuid.New(), Amount: 0}}
	debts := Simplify(bals)
	if len(debts) != 0 {
		t.Fatalf("zero balance should produce 0 debts")
	}
}
