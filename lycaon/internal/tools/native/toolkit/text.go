package toolkit

import (
	"strings"
	"unicode/utf8"
)

// SplitLines splits text into logical lines. A trailing newline does not produce
// an extra empty line. Every tool that counts lines shares this definition.
func SplitLines(text string) []string {
	if text == "" {
		return nil
	}
	lines := strings.Split(text, "\n")
	if strings.HasSuffix(text, "\n") && len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// CountLines returns the number of logical lines in text.
func CountLines(text string) int {
	return len(SplitLines(text))
}

// TrimPartialTrailingRune drops a multi-byte rune that a byte-offset cut left
// half-written. Only the last UTFMax-1 bytes can belong to such a rune, so the
// bound keeps a genuinely invalid tail from being trimmed away.
func TrimPartialTrailingRune(prefix []byte) []byte {
	for i := 0; i < utf8.UTFMax-1 && len(prefix) > 0; i++ {
		if r, size := utf8.DecodeLastRune(prefix); r != utf8.RuneError || size > 1 {
			break
		}
		prefix = prefix[:len(prefix)-1]
	}
	return prefix
}
