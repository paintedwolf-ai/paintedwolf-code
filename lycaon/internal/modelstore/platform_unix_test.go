//go:build darwin || linux

package modelstore

import (
	"errors"
	"math"
	"testing"
)

func TestAvailableDiskBytesSaturatesWithoutOverflow(t *testing.T) {
	for _, tc := range []struct {
		name    string
		blocks  uint64
		size    int64
		want    int64
		invalid bool
	}{
		{"empty", 0, 4096, 0, false},
		{"normal", 3, 4096, 12288, false},
		{"maximum", math.MaxInt64, 1, math.MaxInt64, false},
		{"unsigned", math.MaxUint64, 1, math.MaxInt64, false},
		{"product", math.MaxInt64, 2, math.MaxInt64, false},
		{"overflow", math.MaxUint64, math.MaxInt64, math.MaxInt64, false},
		{"zero block", 1, 0, 0, true},
		{"negative block", 1, -1, 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := availableDiskBytes(tc.blocks, tc.size)
			if got != tc.want || (tc.invalid && !errors.Is(err, ErrInsufficientSpace)) || (!tc.invalid && err != nil) {
				t.Fatalf("available bytes: got %d, %v; want %d, invalid=%v", got, err, tc.want, tc.invalid)
			}
		})
	}
}
