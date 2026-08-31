package balances

import "github.com/google/uuid"

// LedgerEntry is a signed contribution to a user's balance.
// Positive: credit (paid or settlement received). Negative: debt (share or settlement paid).
type LedgerEntry struct {
	UserID uuid.UUID
	Amount int64 // cents, signed
}

// ComputeNet returns net balance per user from ledger entries.
// Invariant: sum of all returned balances == 0 (if ledger is consistent).
func ComputeNet(entries []LedgerEntry) map[uuid.UUID]int64 {
	m := make(map[uuid.UUID]int64)
	for _, e := range entries {
		m[e.UserID] += e.Amount
	}
	return m
}

// ExpenseToEntries converts a single expense into ledger entries.
// payer receives +amount, each participant (split) incurs -share.
// The caller provides amount and splits that already sum to amount (Invariant 1).
func ExpenseToEntries(payer uuid.UUID, amount int64, splits []LedgerEntry) []LedgerEntry {
	entries := make([]LedgerEntry, 0, len(splits)+1)
	entries = append(entries, LedgerEntry{UserID: payer, Amount: amount})
	for _, s := range splits {
		entries = append(entries, LedgerEntry{UserID: s.UserID, Amount: -s.Amount})
	}
	return entries
}

// SettlementToEntries converts a settlement into ledger entries.
// from_user paid to_user: from loses amount (negative is not correct — settlements
// reduce balances). Convention: From's debt decreases if they pay (they get +amount?).
// We use: balance = paid - owed + received - sent.
// So a settlement of X from A to B is: A: +X (they paid, reduces their debt), B: -X.
func SettlementToEntries(from, to uuid.UUID, amount int64) []LedgerEntry {
	return []LedgerEntry{
		{UserID: from, Amount: amount},
		{UserID: to, Amount: -amount},
	}
}
