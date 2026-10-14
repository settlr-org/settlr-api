package money

import (
	"math"
	"strings"
)

// StorageScale is the number of decimal places every stored amount carries:
// amounts are BIGINT hundredths of the major unit for every currency (so ¥1500
// is stored as 150000, KWD 1.25 as 125). Clients (web, Android) use the same
// fixed scale; it is a storage convention, not the currency's ISO exponent.
const StorageScale = 2

// iso4217Exponent lists the ISO 4217 minor-unit exponent of every active
// currency whose exponent is not 2. Unknown codes default to 2.
var iso4217Exponent = map[string]int{
	// 0 decimals
	"BIF": 0, "CLP": 0, "DJF": 0, "GNF": 0, "ISK": 0, "JPY": 0, "KMF": 0, "KRW": 0,
	"PYG": 0, "RWF": 0, "UGX": 0, "UYI": 0, "VND": 0, "VUV": 0, "XAF": 0, "XOF": 0, "XPF": 0,
	// 3 decimals
	"BHD": 3, "IQD": 3, "JOD": 3, "KWD": 3, "LYD": 3, "OMR": 3, "TND": 3,
	// 4 decimals
	"CLF": 4, "UYW": 4,
}

// Exponent returns the ISO 4217 minor-unit exponent of a currency (2 if unknown).
func Exponent(currency string) int {
	if e, ok := iso4217Exponent[strings.ToUpper(strings.TrimSpace(currency))]; ok {
		return e
	}
	return 2
}

// StoredScale returns how many decimal places a stored amount of currency has.
func StoredScale(string) int { return StorageScale }

// Precision is the number of decimals a stored amount of currency can
// meaningfully carry: its ISO exponent, capped by the storage scale.
func Precision(currency string) int {
	return min(Exponent(currency), StoredScale(currency))
}

// ConvertMinor converts a stored amount of `from` into a stored amount of `to`,
// where 1 from = rate to. The result is rounded half away from zero to the
// target currency's real precision (whole yen for JPY, cents for USD, and for
// 3-decimal currencies such as KWD the storage scale), then expressed at the
// target's storage scale.
func ConvertMinor(amount int64, rate float64, from, to string) int64 {
	prec := Precision(to)
	v := float64(amount) * rate * math.Pow10(prec-StoredScale(from))
	return int64(math.Round(v)) * pow10(StoredScale(to)-prec)
}

func pow10(n int) int64 {
	p := int64(1)
	for range n {
		p *= 10
	}
	return p
}
