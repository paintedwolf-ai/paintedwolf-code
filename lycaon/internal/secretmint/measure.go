package secretmint

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// Measurements describe literal credential material without choosing a policy verdict.
type Measurements struct {
	Listed        bool
	Length        int
	DistinctWords int
	LongestRun    int
	RunClasses    int
}

// Measurements uses the inspector that recognized this candidate.
func (c Candidate) Measurements() Measurements { return c.inspector.measure(c.Value) }

func (i *Inspector) measure(value string) Measurements {
	listed := false
	if i != nil {
		_, listed = i.set[strings.ToLower(value)]
	}
	seen := map[string]struct{}{}
	for _, word := range strings.FieldsFunc(value, isWordSeparator) {
		seen[strings.ToLower(word)] = struct{}{}
	}
	longest, classes := longestRun(value)
	return Measurements{Listed: listed, Length: utf8.RuneCountInString(value), DistinctWords: len(seen), LongestRun: longest, RunClasses: classes}
}

// Dollar signs and backticks mark possible shell indirection.
func hasShellIndirection(v string) bool {
	return strings.ContainsAny(v, "$`")
}

func isWordSeparator(r rune) bool {
	return unicode.IsSpace(r) || r == '-' || r == '_' || r == '.' || r == ','
}

// longestRun measures the longest run in the declared key-material alphabet.
func longestRun(v string) (int, int) {
	longest, longestClasses := 0, 0
	run, classes := 0, 0
	for _, r := range v + " " {
		if isRunRune(r) {
			run++
			classes |= classOf(r)
			continue
		}
		if run > longest || (run == longest && popcount(classes) > popcount(longestClasses)) {
			longest, longestClasses = run, classes
		}
		run, classes = 0, 0
	}
	return longest, popcount(longestClasses)
}

func isRunRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '_' || r == '+' || r == '/' || r == '='
}

// Punctuation does not add character variety to a run.
func classOf(r rune) int {
	switch {
	case unicode.IsLower(r):
		return 1
	case unicode.IsUpper(r):
		return 2
	case unicode.IsDigit(r):
		return 4
	default:
		return 0
	}
}

func popcount(mask int) int {
	n := 0
	for mask != 0 {
		n += mask & 1
		mask >>= 1
	}
	return n
}
