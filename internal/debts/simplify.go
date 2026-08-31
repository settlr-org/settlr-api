package debts

import (
	"sort"

	"github.com/google/uuid"
)

// Balance represents a member's net balance in a group (cents).
// Positive = others owe this user. Negative = this user owes others.
// Sum of all balances must be 0 (Invariant 2).
type Balance struct {
	UserID uuid.UUID
	Amount int64 // signed cents
}

// Debt is a directed edge: From owes To amount.
type Debt struct {
	From   uuid.UUID `json:"from_user"`
	To     uuid.UUID `json:"to_user"`
	Amount int64     `json:"amount"`
}

// Simplify computes a minimal (greedy) set of settlement edges that settles
// all balances. It conserves money: sum of debt amounts == sum of positive balances.
// Returns at most n-1 debts for n participants with non-zero balances.
// Deterministic: sorts creditors/debtors by magnitude, tie-break by UUID string.
func Simplify(balances []Balance) []Debt {
	type entry struct {
		id     uuid.UUID
		amount int64 // always positive: creditor amount or debtor debt magnitude
	}

	var creditors []entry
	var debtors []entry

	for _, b := range balances {
		if b.Amount > 0 {
			creditors = append(creditors, entry{b.UserID, b.Amount})
		} else if b.Amount < 0 {
			debtors = append(debtors, entry{b.UserID, -b.Amount})
		}
	}

	less := func(a, b entry) bool {
		if a.amount != b.amount {
			return a.amount > b.amount
		}
		return a.id.String() < b.id.String()
	}
	sort.Slice(creditors, func(i, j int) bool { return less(creditors[i], creditors[j]) })
	sort.Slice(debtors, func(i, j int) bool { return less(debtors[i], debtors[j]) })

	var debts []Debt
	i, j := 0, 0
	for i < len(creditors) && j < len(debtors) {
		c := &creditors[i]
		d := &debtors[j]

		amt := c.amount
		if d.amount < amt {
			amt = d.amount
		}

		debts = append(debts, Debt{
			From:   d.id,
			To:     c.id,
			Amount: amt,
		})

		c.amount -= amt
		d.amount -= amt
		if c.amount == 0 {
			i++
		}
		if d.amount == 0 {
			j++
		}
	}
	if debts == nil {
		return []Debt{}
	}
	return debts
}
