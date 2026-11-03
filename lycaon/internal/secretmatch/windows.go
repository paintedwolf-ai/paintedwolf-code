package secretmatch

import (
	"strings"
	"unicode/utf8"
)

// windowEnd prefers a line break and preserves rune boundaries.
func windowEnd(s string, start, window int) int {
	end := start + window
	if end >= len(s) {
		return len(s)
	}
	if nl := strings.LastIndexByte(s[start:end], '\n'); nl > 0 {
		return start + nl + 1
	}
	return originalRuneBoundary(s, end)
}

// windowNextStart preserves overlap while advancing the window.
func windowNextStart(s string, start, end, overlap int) int {
	next := end - overlap
	if next <= start {
		return end
	}
	if nl := strings.IndexByte(s[next:end], '\n'); nl >= 0 && next+nl+1 < end {
		return next + nl + 1
	}
	boundary := originalRuneBoundary(s, next)
	if boundary < next {
		_, width := utf8.DecodeRuneInString(s[boundary:])
		next = boundary + width
	}
	return next
}

// originalRuneBoundary backs off only inside a valid multi-byte rune. Invalid
// continuation bytes are independent original bytes and remain valid boundaries.
func originalRuneBoundary(s string, at int) int {
	if at <= 0 || at >= len(s) {
		return at
	}
	for i := at - 1; i >= max(0, at-utf8.UTFMax+1); i-- {
		if utf8.RuneStart(s[i]) {
			_, width := utf8.DecodeRuneInString(s[i:])
			if width > 1 && i+width > at {
				return i
			}
			return at
		}
	}
	return at
}
