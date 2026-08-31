package money

import (
	"errors"
	"hash/fnv"
	"sort"
)

// hashString is FNV-1a 32-bit, matching Spliit src/lib/shares.ts:hashString
func hashString(s string) uint32 {
	h := fnv.New32a()
	_, _ = h.Write([]byte(s))
	return h.Sum32()
}

// apportion distributes amount over shares with Hamilton largest-remainder,
// rotating tie-break via offset (hash % n) so even splits don't always favour index 0.
func apportion(amount int64, shares []int64, totalShares int64, offset int) []int64 {
	n := len(shares)
	if n == 0 {
		return []int64{}
	}
	if totalShares == 0 {
		out := make([]int64, n)
		return out
	}
	amounts := make([]int64, n)
	remainders := make([]int64, n)
	var distributed int64
	for i, sh := range shares {
		numer := amount * sh
		// floor for positive amounts (amount>0, shares>0) is numer/totalShares
		base := numer / totalShares
		// for negative amounts (income) Math.floor in JS keeps remainder in [0,total) — same as Go integer division for positive only
		// we keep Go truncating division since amount>0 validated
		amounts[i] = base
		remainders[i] = numer % totalShares
		distributed += base
	}
	remaining := amount - distributed
	if remaining == 0 {
		return amounts
	}
	type idxRem struct {
		idx int
		rem int64
	}
	order := make([]idxRem, n)
	for i, r := range remainders {
		order[i] = idxRem{i, r}
	}
	sort.Slice(order, func(a, b int) bool {
		if order[a].rem != order[b].rem {
			return order[a].rem > order[b].rem
		}
		rotate := func(idx int) int { return (idx - offset + n) % n }
		return rotate(order[a].idx) < rotate(order[b].idx)
	})
	for i := int64(0); i < remaining && i < int64(n); i++ {
		amounts[order[i].idx]++
	}
	return amounts
}

// SplitEqual divides amount (in smallest currency unit) equally among n participants.
// Uses Hamilton apportionment with offset 0 (legacy deterministic).
func SplitEqual(amount int64, n int) ([]int64, error) {
	return SplitEqualWithID(amount, n, "")
}

// SplitEqualWithID is deterministic per expense ID via FNV-1a rotation, matching Spliit.
// If id == "" offset 0 (fallback).
func SplitEqualWithID(amount int64, n int, id string) ([]int64, error) {
	if n <= 0 {
		return nil, errors.New("n must be > 0")
	}
	if amount <= 0 {
		return nil, errors.New("amount must be > 0")
	}
	shares := make([]int64, n)
	for i := range shares {
		shares[i] = 1
	}
	offset := 0
	if id != "" && n > 0 {
		offset = int(hashString(id) % uint32(n))
	}
	return apportion(amount, shares, int64(n), offset), nil
}

// SplitExact validates that shares sum to amount and returns a copy of shares.
func SplitExact(amount int64, shares []int64) ([]int64, error) {
	if len(shares) == 0 {
		return nil, errors.New("shares must not be empty")
	}
	var sum int64
	for _, s := range shares {
		if s < 0 {
			return nil, errors.New("share amount must be >= 0")
		}
		sum += s
	}
	if sum != amount {
		return nil, errors.New("split amounts must sum to total amount")
	}
	out := make([]int64, len(shares))
	copy(out, shares)
	return out, nil
}

// SplitByPercentage splits amount according to percentages expressed in basis
// points (10000 = 100%). Uses Hamilton with offset 0.
func SplitByPercentage(amount int64, pctBps []int64) ([]int64, error) {
	return SplitByPercentageWithID(amount, pctBps, "")
}

// SplitByPercentageWithID uses FNV rotation for ties.
func SplitByPercentageWithID(amount int64, pctBps []int64, id string) ([]int64, error) {
	if len(pctBps) == 0 {
		return nil, errors.New("percentages must not be empty")
	}
	if amount <= 0 {
		return nil, errors.New("amount must be > 0")
	}
	var sumBps int64
	for _, p := range pctBps {
		if p < 0 {
			return nil, errors.New("percentage must be >= 0")
		}
		sumBps += p
	}
	if sumBps != 10000 {
		return nil, errors.New("percentages must sum to 10000 basis points (100%)")
	}
	n := len(pctBps)
	offset := 0
	if id != "" && n > 0 {
		offset = int(hashString(id) % uint32(n))
	}
	return apportion(amount, pctBps, 10000, offset), nil
}

// SplitByShares splits amount proportionally to shares (each share > 0).
// Uses Hamilton with offset 0.
func SplitByShares(amount int64, shares []int64) ([]int64, error) {
	return SplitBySharesWithID(amount, shares, "")
}

// SplitBySharesWithID uses FNV rotation.
func SplitBySharesWithID(amount int64, shares []int64, id string) ([]int64, error) {
	if len(shares) == 0 {
		return nil, errors.New("shares must not be empty")
	}
	if amount <= 0 {
		return nil, errors.New("amount must be > 0")
	}
	var totalShares int64
	for _, s := range shares {
		if s <= 0 {
			return nil, errors.New("each share must be > 0")
		}
		totalShares += s
	}
	if totalShares == 0 {
		return nil, errors.New("total shares must be > 0")
	}
	n := len(shares)
	offset := 0
	if id != "" && n > 0 {
		offset = int(hashString(id) % uint32(n))
	}
	return apportion(amount, shares, totalShares, offset), nil
}
