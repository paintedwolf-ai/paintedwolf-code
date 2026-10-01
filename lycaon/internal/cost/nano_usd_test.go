package cost

import (
	"math"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestUSDToNanoRoundsToTheNearestNanoDollar(t *testing.T) {
	for usd, want := range map[float64]int64{0: 0, 0.1: 100_000_000, 12.34: 12_340_000_000, 1e-9: 1, 4e-10: 0, 6e-10: 1} {
		got, err := USDToNano(usd)
		testutil.FailErr(t, "convert USD", err)
		if got != want {
			t.Errorf("USDToNano(%v) = %d, want %d", usd, got, want)
		}
		if back, _ := USDToNano(NanoToUSD(got)); back != got {
			t.Errorf("round trip of %d nano-dollars gave %d", got, back)
		}
	}
}

func TestUSDToNanoRejectsUnrepresentableAmounts(t *testing.T) {
	for _, usd := range []float64{-0.01, math.NaN(), math.Inf(1), math.Inf(-1), 1e10} {
		if _, err := USDToNano(usd); err == nil {
			t.Errorf("USDToNano(%v) accepted", usd)
		}
	}
}
