//go:build stress

package sourcecomparison

import "testing"

func TestStressComparisonReservationConvergesForLargeShortLines(t *testing.T) {
	assertComparisonReservationConverges(t, 4<<20)
}
