// Package sourceloc reads the file locations compilers, test runners, code hosts, and people
// write after a path.
package sourceloc

import (
	"regexp"
	"strconv"
	"strings"
)

// Location is a one-based line with an optional column or inclusive end line; zero means absent.
type Location struct {
	Line, Column, EndLine int
}

type pattern struct {
	re *regexp.Regexp
	// Submatch indexes of the line, column, and end line; zero when absent.
	line, column, end int
}

// Trailing locations: `:12:3`, `:10-20`, `#L12C3`, `#L12-L20`, `#lines-10:20`, `(12,3)`.
// Fragments come first because `#lines-10:20` also ends in a colon form.
var patterns = []pattern{
	{re: regexp.MustCompile(`#L(\d+)(?:C(\d+))?(?:-L?(\d+)(?:C\d+)?)?$`), line: 1, column: 2, end: 3},
	{re: regexp.MustCompile(`#lines-(\d+)(?::(\d+))?$`), line: 1, end: 2},
	{re: regexp.MustCompile(`:(\d+)-(\d+)$`), line: 1, end: 2},
	{re: regexp.MustCompile(`:(\d+)(?::(\d+))?$`), line: 1, column: 2},
	{re: regexp.MustCompile(`\((\d+)(?:,\s*(\d+))?\)$`), line: 1, column: 2},
}

// Cut splits a trailing location from text. A location with nothing before it is text.
func Cut(text string) (string, Location, bool) {
	for _, p := range patterns {
		match := p.re.FindStringSubmatchIndex(text)
		if len(match) == 0 || match[0] == 0 {
			continue
		}
		// `name (1).pdf` is a file name, not a location.
		if text[match[0]] == '(' && strings.ContainsRune(" \t", rune(text[match[0]-1])) {
			continue
		}
		number := func(group int) int {
			if group == 0 || match[2*group] < 0 {
				return 0
			}
			n, err := strconv.Atoi(text[match[2*group]:match[2*group+1]])
			if err != nil || n < 1 {
				return 0
			}
			return n
		}
		loc := Location{Line: number(p.line), Column: number(p.column), EndLine: number(p.end)}
		if loc.Line == 0 {
			continue
		}
		if loc.EndLine < loc.Line {
			loc.EndLine = 0
		}
		return text[:match[0]], loc, true
	}
	return text, Location{}, false
}
