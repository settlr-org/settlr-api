package balances

import (
	"math/rand"
	"testing"

	"github.com/google/uuid"
)

func randomLedger(t *testing.T, nExpenses int) ([]LedgerEntry, []LedgerEntry) {
	t.Helper()
	users := make([]uuid.UUID, 5)
	for i := range users {
		users[i] = uuid.New()
	}
	var entries []LedgerEntry
	for i := 0; i < nExpenses; i++ {
		payer := users[rand.Intn(len(users))]
		amount := int64(rand.Intn(10000) + 1) // 1..10000 cents
		// random participants subset 1..len(users)
		k := rand.Intn(len(users)) + 1
		perm := rand.Perm(len(users))[:k]
		participants := make([]LedgerEntry, k)
		for j, idx := range perm {
			participants[j] = LedgerEntry{UserID: users[idx]}
		}
		// equal split for property test
		base := amount / int64(k)
		rem := amount % int64(k)
		splits := make([]LedgerEntry, k)
		for j := range splits {
			amt := base
			if int64(j) < rem {
				amt++
			}
			splits[j] = LedgerEntry{UserID: participants[j].UserID, Amount: amt}
		}
		entries = append(entries, ExpenseToEntries(payer, amount, splits)...)
	}
	// random settlements
	var settlements []LedgerEntry
	for i := 0; i < rand.Intn(3); i++ {
		from := users[rand.Intn(len(users))]
		to := users[rand.Intn(len(users))]
		if from == to {
			continue
		}
		amt := int64(rand.Intn(5000) + 1)
		settlements = append(settlements, SettlementToEntries(from, to, amt)...)
	}
	entries = append(entries, settlements...)
	return entries, nil
}

func TestProperty_SumZero(t *testing.T) {
	for iter := 0; iter < 200; iter++ {
		entries, _ := randomLedger(t, 5)
		m := ComputeNet(entries)
		var sum int64
		for _, v := range m {
			sum += v
		}
		if sum != 0 {
			t.Fatalf("iter %d sum %d !=0 m %v", iter, sum, m)
		}
	}
}

func TestProperty_DeleteIsNeverExisted(t *testing.T) {
	for iter := 0; iter < 200; iter++ {
		alice := uuid.New()
		bob := uuid.New()
		exp1 := ExpenseToEntries(alice, 10000, []LedgerEntry{{UserID: alice, Amount: 5000}, {UserID: bob, Amount: 5000}})
		exp2 := ExpenseToEntries(bob, 6000, []LedgerEntry{{UserID: alice, Amount: 3000}, {UserID: bob, Amount: 3000}})
		ledgerBoth := append(append([]LedgerEntry{}, exp1...), exp2...)
		ledgerOnlySecond := exp2
		mBoth := ComputeNet(ledgerBoth)
		mSecond := ComputeNet(ledgerOnlySecond)
		var sumBoth, sumSecond int64
		for _, v := range mBoth {
			sumBoth += v
		}
		for _, v := range mSecond {
			sumSecond += v
		}
		if sumBoth != 0 || sumSecond != 0 {
			t.Fatalf("sums not zero %d %d", sumBoth, sumSecond)
		}
		// Deleting first expense should equal never existed: ledgerOnlySecond
		// Already verified both sum zero; also check individual balances match expectation
		if mSecond[alice] != -3000 || mSecond[bob] != 3000 {
			t.Fatalf("unexpected balances after delete %v", mSecond)
		}
		_ = iter
	}
}

func TestProperty_EditIsDeletePlusCreate(t *testing.T) {
	for iter := 0; iter < 200; iter++ {
		alice := uuid.New()
		bob := uuid.New()
		// old expense: alice paid 10000 split 5000/5000
		oldEntries := ExpenseToEntries(alice, 10000, []LedgerEntry{{UserID: alice, Amount: 5000}, {UserID: bob, Amount: 5000}})
		newEntries := ExpenseToEntries(bob, 8000, []LedgerEntry{{UserID: alice, Amount: 4000}, {UserID: bob, Amount: 4000}})
		other := ExpenseToEntries(alice, 6000, []LedgerEntry{{UserID: alice, Amount: 3000}, {UserID: bob, Amount: 3000}})
		ledgerA := append(append([]LedgerEntry{}, other...), oldEntries...)
		ledgerB := append(append([]LedgerEntry{}, other...), newEntries...)
		mA := ComputeNet(ledgerA)
		mB := ComputeNet(ledgerB)
		var sumA, sumB int64
		for _, v := range mA {
			sumA += v
		}
		for _, v := range mB {
			sumB += v
		}
		if sumA != 0 || sumB != 0 {
			t.Fatalf("sums not zero %d %d", sumA, sumB)
		}
		// Delta should be new - old
		if mB[alice]-mA[alice] != -9000 {
			t.Fatalf("delta alice %d expected -9000", mB[alice]-mA[alice])
		}
		_ = iter
	}
}
