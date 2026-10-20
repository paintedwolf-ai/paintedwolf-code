//go:build linux

package launchguard

import "testing"

func TestFullIdentityMapRejectsRemappedOrAdditionalRanges(t *testing.T) {
	for _, tc := range []struct {
		value string
		valid bool
	}{
		{"         0          0 4294967295\n", true},
		{"0 1000 1", false},
		{"1000 1000 1", false},
		{"0 0 4294967295\n1000 1000 1", false},
		{"0 0 4294967296", false},
		{"0 0 -1", false},
		{"", false},
	} {
		if got := fullIdentityMap(tc.value); got != tc.valid {
			t.Errorf("mapping %q: got %v, want %v", tc.value, got, tc.valid)
		}
	}
}
