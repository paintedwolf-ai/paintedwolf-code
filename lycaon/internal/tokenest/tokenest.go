// Package tokenest owns text measurement: a shared character proxy for budget
// estimates and embedded tokenizers for declared ordinary-text encodings.
// Providers report full prompt usage; compaction.PromptTokenCalibration calibrates
// budget estimates against those observations.
package tokenest

import "unicode/utf8"

// DefaultDivisor is the host's characters-per-token proxy.
const DefaultDivisor = 4

// Estimate returns the ceiling of rune count over divisor. A non-positive
// divisor falls back to DefaultDivisor. Non-empty text costs at least 1, so a
// short identity line cannot admit unbounded rows under a finite budget.
func Estimate(s string, divisor int) int {
	if divisor <= 0 {
		divisor = DefaultDivisor
	}
	n := utf8.RuneCountInString(s)
	if n == 0 {
		return 0
	}
	return (n + divisor - 1) / divisor
}

// EstimateDefault measures s at the host divisor.
func EstimateDefault(s string) int { return Estimate(s, DefaultDivisor) }

// EstimateBytes measures raw UTF-8 bytes without copying them to a string.
func EstimateBytes(raw []byte, divisor int) int {
	if divisor <= 0 {
		divisor = DefaultDivisor
	}
	n := utf8.RuneCount(raw)
	if n == 0 {
		return 0
	}
	return (n + divisor - 1) / divisor
}

// FromUnitCount applies the same ceiling to a count a caller already tallied.
// Callers that counted bytes rather than runes get a byte-scaled estimate; the
// two agree on ASCII and the byte form over-counts multi-byte text, which is the
// safe direction for a budget.
func FromUnitCount(units, divisor int) int {
	if divisor <= 0 {
		divisor = DefaultDivisor
	}
	if units <= 0 {
		return 0
	}
	return (units + divisor - 1) / divisor
}
