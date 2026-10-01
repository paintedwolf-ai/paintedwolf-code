// Package runeclamp bounds display text to a rune budget and marks the cut.
package runeclamp

import (
	"strings"
	"unicode/utf8"
)

// Marker marks omitted text.
const Marker = "…"

// Clamp keeps at most max runes plus Marker when truncated.
func Clamp(s string, max int) string {
	if max <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return cut(string(r[:max]))
}

// Fit counts Marker within the rune budget.
func Fit(s string, max int) string {
	if max <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	keep := max - utf8.RuneCountInString(Marker)
	if keep <= 0 {
		return string(r[:max])
	}
	return cut(string(r[:keep]))
}

// Trailing horizontal whitespace is removed before the marker.
func cut(kept string) string {
	return strings.TrimRight(kept, " \t") + Marker
}
