// Package runematch finds literal text in rune sequences in linear time.
package runematch

import (
	"context"
	"unicode"
)

// Pattern finds non-overlapping occurrences of a literal rune sequence.
type Pattern struct {
	query     []rune
	prefix    []int
	sensitive bool
}

// Compile prepares query for matching. Case-insensitive matching folds each
// rune with unicode.ToLower, so matches keep the query's rune length.
func Compile(query string, caseSensitive bool) Pattern {
	runes := []rune(query)
	if !caseSensitive {
		for i := range runes {
			runes[i] = unicode.ToLower(runes[i])
		}
	}
	// The failure table makes repeated-prefix queries linear in the text size.
	prefix := make([]int, len(runes))
	for i, matched := 1, 0; i < len(runes); i++ {
		for matched > 0 && runes[i] != runes[matched] {
			matched = prefix[matched-1]
		}
		if runes[i] == runes[matched] {
			matched++
		}
		prefix[i] = matched
	}
	return Pattern{query: runes, prefix: prefix, sensitive: caseSensitive}
}

// Len is the query length in runes.
func (p Pattern) Len() int { return len(p.query) }

// Scan reports each match start in text at or after from until visit returns
// false. It returns the position just past the last rune examined, which is
// where a later scan resumes.
func (p Pattern) Scan(ctx context.Context, text []rune, from int, visit func(start int) bool) (int, error) {
	if len(p.query) == 0 {
		return len(text), nil
	}
	at, matched := from, 0
	for at < len(text) {
		if at%4096 == 0 {
			if err := ctx.Err(); err != nil {
				return at, err
			}
		}
		actual := text[at]
		if !p.sensitive {
			actual = unicode.ToLower(actual)
		}
		for matched > 0 && actual != p.query[matched] {
			matched = p.prefix[matched-1]
		}
		if actual == p.query[matched] {
			matched++
		}
		at++
		if matched != len(p.query) {
			continue
		}
		matched = 0
		if !visit(at - len(p.query)) {
			break
		}
	}
	return at, nil
}
