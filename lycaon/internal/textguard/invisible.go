// Package textguard strips invisible and control codepoints from untrusted text before model evaluation.
package textguard

import (
	"strings"
	"unicode"
)

// InvisibleFormatRune reports whether r is an invisible or control codepoint.
// Tabs and newlines are preserved.
func InvisibleFormatRune(r rune) bool {
	switch {
	case r == '\t' || r == '\n':
		return false // handled as whitespace collapse elsewhere
	case r < 0x20 || r == 0x7f:
		return true
	case r >= 0xE0000 && r <= 0xE007F: // Unicode Tags
		return true
	case r == 0x200B || r == 0x200C || r == 0x200D || r == 0xFEFF || r == 0x2060:
		return true
	case r >= 0x202A && r <= 0x202E:
		return true
	case r >= 0x2066 && r <= 0x2069:
		return true
	case unicode.Is(unicode.Cf, r) && !unicode.IsSpace(r):
		return true
	default:
		return false
	}
}

// StripInvisibleFormatRunesUntilStable removes InvisibleFormatRune codepoints, repeating
// until stable (≤4 passes) so that removing one layer cannot reveal another. Preserves
// tabs and newlines. Does not strip markup or collapse whitespace.
func StripInvisibleFormatRunesUntilStable(s string) string {
	prev := ""
	cur := s
	for i := 0; i < 4 && cur != prev; i++ {
		prev = cur
		var b strings.Builder
		b.Grow(len(cur))
		for _, r := range cur {
			if InvisibleFormatRune(r) {
				continue
			}
			b.WriteRune(r)
		}
		cur = b.String()
	}
	return cur
}
