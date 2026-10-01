package cost

import (
	"fmt"
	"math"
)

// NanoPerUSD is the store's money unit: amounts persist as integer nano-dollars.
const NanoPerUSD = 1_000_000_000

// USDToNano converts a dollar amount from a float source (provider price
// feeds, overlay YAML) to nano-dollars. Negative, non-finite, and overflowing
// amounts are errors.
func USDToNano(usd float64) (int64, error) {
	if math.IsNaN(usd) || math.IsInf(usd, 0) || usd < 0 || usd >= float64(math.MaxInt64)/NanoPerUSD {
		return 0, fmt.Errorf("invalid USD amount %v", usd)
	}
	return int64(math.Round(usd * NanoPerUSD)), nil
}

// NanoToUSD converts nano-dollars to a float dollar amount for computation
// against float rates; wire fields carry the integer nano-dollars.
func NanoToUSD(nano int64) float64 {
	return float64(nano) / NanoPerUSD
}
