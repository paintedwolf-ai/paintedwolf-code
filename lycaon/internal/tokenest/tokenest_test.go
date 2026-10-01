package tokenest_test

import (
	"testing"

	"github.com/lycaon/lycaon/internal/tokenest"
)

func TestEstimateIsACeiling(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in      string
		divisor int
		want    int
	}{
		{"", 4, 0},
		{"a", 4, 1},
		{"abc", 4, 1},
		{"abcd", 4, 1},
		{"abcde", 4, 2},
		{"abcd", 1, 4},
		{"abcd", 0, 1},  // non-positive divisor falls back to the host default
		{"abcd", -3, 1}, // same
	}
	for _, tc := range cases {
		if got := tokenest.Estimate(tc.in, tc.divisor); got != tc.want {
			t.Errorf("Estimate(%q, %d) = %d want %d", tc.in, tc.divisor, got, tc.want)
		}
	}
}

// Runes, not bytes: a budget measured in bytes would shrink by a factor of three
// on CJK text and admit far less than it was told to.
func TestEstimateCountsRunes(t *testing.T) {
	t.Parallel()
	const four = "日本語で" // 4 runes, 12 bytes
	if got := tokenest.EstimateDefault(four); got != 1 {
		t.Fatalf("EstimateDefault(%q) = %d want 1", four, got)
	}
	if got := tokenest.EstimateBytes([]byte(four), tokenest.DefaultDivisor); got != 1 {
		t.Fatalf("EstimateBytes(%q) = %d want 1", four, got)
	}
}

// The three entry points are one formula: a budget one caller computes is spent
// by another.
func TestAllEntryPointsAgree(t *testing.T) {
	t.Parallel()
	for _, s := range []string{"", "a", "hello world", "日本語で書かれた文章", "mixed ascii と日本語"} {
		text := tokenest.EstimateDefault(s)
		raw := tokenest.EstimateBytes([]byte(s), tokenest.DefaultDivisor)
		if text != raw {
			t.Errorf("%q: EstimateDefault=%d EstimateBytes=%d", s, text, raw)
		}
		if got := tokenest.Estimate(s, tokenest.DefaultDivisor); got != text {
			t.Errorf("%q: Estimate=%d EstimateDefault=%d", s, got, text)
		}
	}
	if got, want := tokenest.FromUnitCount(5, 4), tokenest.EstimateDefault("abcde"); got != want {
		t.Errorf("FromUnitCount(5,4) = %d want %d", got, want)
	}
	if got := tokenest.FromUnitCount(0, 4); got != 0 {
		t.Errorf("FromUnitCount(0,4) = %d want 0", got)
	}
}
