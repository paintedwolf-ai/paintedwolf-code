package toolkit

import (
	"strings"
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
