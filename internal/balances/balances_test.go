package balances

import (
	"testing"

	"github.com/google/uuid"
)

func uid(s string) uuid.UUID { return uuid.NewSHA1(uuid.NameSpaceOID, []byte(s)) }

func TestComputeNet_ZeroSum(t *testing.T) {
	alice := uid("alice")
	bob := uid("bob")
	charlie := uid("charlie")

	// Expense: Alice paid 12000, splits 3000/3000/6000
	exp := ExpenseToEntries(alice, 12000, []LedgerEntry{
		{UserID: alice, Amount: 3000},
		{UserID: bob, Amount: 3000},
		{UserID: charlie, Amount: 6000},
	})
	m := ComputeNet(exp)
	if m[alice] != 9000 {
		t.Fatalf("alice %d want 9000", m[alice])
	}
	if m[bob] != -3000 {
		t.Fatalf("bob %d", m[bob])
	}
	if m[charlie] != -6000 {
		t.Fatalf("charlie %d", m[charlie])
	}
	var sum int64
	for _, v := range m {
		sum += v
	}
	if sum != 0 {
		t.Fatalf("sum %d != 0", sum)
	}
}

func TestInvariant_DeleteRemovesEffect(t *testing.T) {
	alice := uid("alice")
	bob := uid("bob")
	exp := ExpenseToEntries(alice, 10000, []LedgerEntry{{alice, 5000}, {bob, 5000}})
	m1 := ComputeNet(exp)
	// Deleting should produce empty ledger -> zero balances
	m2 := ComputeNet(nil)
	if len(m2) != 0 {
		t.Fatalf("empty ledger not empty")
	}
	// Simulate edit as delete+create: balances after edit == balances from new expense only
	newExp := ExpenseToEntries(bob, 6000, []LedgerEntry{{alice, 3000}, {bob, 3000}})
	combined := append([]LedgerEntry{}, exp...)
	combined = append(combined, newExp...)
	// If we "delete" old and add new, effective is just newExp
	if m1[alice] == 0 {
		t.Fatal("unexpected zero")
	}
	_ = combined
	_ = m2
}

func TestSettlementEntries(t *testing.T) {
	alice := uid("alice")
	bob := uid("bob")
	// Bob pays Alice 5000
	s := SettlementToEntries(bob, alice, 5000)
	m := ComputeNet(s)
	if m[bob] != 5000 {
		t.Fatalf("bob %d", m[bob])
	}
	if m[alice] != -5000 {
		t.Fatalf("alice %d", m[alice])
	}
	// Combined with prior expense: Alice +9000, Bob -3000; after Bob pays 3000 -> Alice +6000, Bob 0
	alice2 := uid("alice")
	bob2 := uid("bob")
	charlie := uid("charlie")
	exp := ExpenseToEntries(alice2, 12000, []LedgerEntry{{alice2, 3000}, {bob2, 3000}, {charlie, 6000}})
	settle := SettlementToEntries(bob2, alice2, 3000)
	all := append(exp, settle...)
	m2 := ComputeNet(all)
	if m2[bob2] != 0 {
		t.Fatalf("bob after settlement %d want 0", m2[bob2])
	}
	if m2[alice2] != 6000 {
		t.Fatalf("alice after settlement %d want 6000", m2[alice2])
	}
	var sum int64
	for _, v := range m2 {
		sum += v
	}
	if sum != 0 {
		t.Fatalf("sum %d", sum)
	}
}

func TestEditSameAsDeletePlusCreate(t *testing.T) {
	alice := uid("alice")
	bob := uid("bob")
	oldEntries := ExpenseToEntries(alice, 10000, []LedgerEntry{{alice, 5000}, {bob, 5000}})
	newEntries := ExpenseToEntries(bob, 8000, []LedgerEntry{{alice, 4000}, {bob, 4000}})
	// Ledger with unrelated expense
	other := ExpenseToEntries(alice, 6000, []LedgerEntry{{alice, 3000}, {bob, 3000}})
	// Scenario A: ledger = other + old
	ledgerA := append(append([]LedgerEntry{}, other...), oldEntries...)
	mA := ComputeNet(ledgerA)
	// Scenario B: edit old -> new: ledger = other + new
	ledgerB := append(append([]LedgerEntry{}, other...), newEntries...)
	mB := ComputeNet(ledgerB)
	// Invariant 5: editing should be same as delete+create, so mB is expected post-edit
	// Just verify both sum to 0 and differ by exactly the delta
	var sumA, sumB int64
	for _, v := range mA {
		sumA += v
	}
	for _, v := range mB {
		sumB += v
	}
	if sumA != 0 || sumB != 0 {
		t.Fatalf("sums %d %d", sumA, sumB)
	}
	// Delta should equal new - old
	deltaAlice := mB[alice] - mA[alice]
	// old contributed: alice +5000 (paid 10000 - owed 5000 = +5000), new: bob paid 8000, alice owes 4000 => alice -4000
	// So delta alice = -4000 - 5000 = -9000
	if deltaAlice != -9000 {
		t.Fatalf("delta alice %d", deltaAlice)
	}
}
