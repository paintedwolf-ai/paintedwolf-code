package projectsource

import (
	"regexp"
	"strings"
	"unicode"
)

// symbolMatchTier orders how a declaration name answers a query; lower ranks first.
type symbolMatchTier int

const (
	// symbolTierExactCase is the query's own spelling.
	symbolTierExactCase symbolMatchTier = iota
	// symbolTierExact is the whole name, ignoring case.
	symbolTierExact
	symbolTierPrefix
	// symbolTierWordStart is a contiguous match beginning at a word of the name.
	symbolTierWordStart
	// symbolTierHump abbreviates the name's words ("pc", "ParseCfg" for ParseConfig).
	symbolTierHump
	symbolTierSubstring
	symbolTierNone
)

const (
	// symbolHumpNameLimit bounds the abbreviation search per name.
	symbolHumpNameLimit = 256
	// symbolHumpPatternLimit bounds the discovery expression, which grows
	// quadratically with the query.
	symbolHumpPatternLimit = 16
)

// symbolNameMatcher compares declaration names with one query, ignoring case.
type symbolNameMatcher struct {
	query  string
	folded []rune
}

func newSymbolNameMatcher(query string) symbolNameMatcher {
	return symbolNameMatcher{query: query, folded: foldSymbolRunes([]rune(query))}
}

// match returns the name's tier and the matched ranges as code point offsets.
func (m symbolNameMatcher) match(name string) (symbolMatchTier, []SourceTextRange) {
	runes := []rune(name)
	query := m.folded
	if len(query) == 0 || len(query) > len(runes) {
		return symbolTierNone, nil
	}
	folded := foldSymbolRunes(runes)
	whole := []SourceTextRange{{Start: 0, End: len(query)}}
	if len(query) == len(runes) && runesEqual(folded, query) {
		if name == m.query {
			return symbolTierExactCase, whole
		}
		return symbolTierExact, whole
	}
	if runesEqual(folded[:len(query)], query) {
		return symbolTierPrefix, whole
	}
	starts := symbolWordStarts(runes)
	substring := -1
	for at := 1; at+len(query) <= len(folded); at++ {
		if !runesEqual(folded[at:at+len(query)], query) {
			continue
		}
		if starts[at] {
			return symbolTierWordStart, []SourceTextRange{{Start: at, End: at + len(query)}}
		}
		if substring < 0 {
			substring = at
		}
	}
	if positions := symbolHumpPositions(folded, starts, query); positions != nil {
		return symbolTierHump, rangesOfPositions(positions)
	}
	if substring >= 0 {
		return symbolTierSubstring, []SourceTextRange{{Start: substring, End: substring + len(query)}}
	}
	return symbolTierNone, nil
}

// spelledExactly reports whether the name's matched characters, in order,
// are the query as typed.
func (m symbolNameMatcher) spelledExactly(name string, highlights []SourceTextRange) bool {
	runes := []rune(name)
	spelled := make([]rune, 0, len(m.folded))
	for _, span := range highlights {
		if span.Start < 0 || span.End > len(runes) || span.Start > span.End {
			return false
		}
		spelled = append(spelled, runes[span.Start:span.End]...)
	}
	return string(spelled) == m.query
}

func foldSymbolRunes(runes []rune) []rune {
	out := make([]rune, len(runes))
	for i, r := range runes {
		out[i] = unicode.ToLower(r)
	}
	return out
}

func runesEqual(a, b []rune) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func isSymbolAlnum(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r)
}

// symbolWordStarts marks where each word of a name begins: after a separator,
// at a lower-to-upper step, at the last capital of a run before lowercase
// ("HTTPServer" → HTTP, Server), and where letters and digits meet.
func symbolWordStarts(name []rune) []bool {
	starts := make([]bool, len(name))
	for i, r := range name {
		if !isSymbolAlnum(r) {
			continue
		}
		if i == 0 {
			starts[i] = true
			continue
		}
		prev := name[i-1]
		switch {
		case !isSymbolAlnum(prev):
			starts[i] = true
		case unicode.IsUpper(r) && !unicode.IsUpper(prev):
			starts[i] = true
		case unicode.IsUpper(r) && i+1 < len(name) && unicode.IsLower(name[i+1]):
			starts[i] = true
		case unicode.IsDigit(r) != unicode.IsDigit(prev):
			starts[i] = true
		}
	}
	return starts
}

// symbolHumpPositions matches every query rune in order: the first begins a
// word, each later one continues within the current word or begins a later
// word, and at least one begins a later word. Positions prefer contiguous
// runs, then the earliest word.
func symbolHumpPositions(folded []rune, starts []bool, query []rune) []int {
	if len(query) < 2 || len(folded) > symbolHumpNameLimit || !isRuneSubsequence(query, folded) {
		return nil
	}
	nextStart := make([]int, len(folded))
	next := len(folded)
	for i := len(folded) - 1; i >= 0; i-- {
		nextStart[i] = next
		if starts[i] {
			next = i
		}
	}
	type state struct {
		j, prev int
		jumped  bool
	}
	failed := map[state]bool{}
	positions := make([]int, len(query))
	var walk func(j, prev int, jumped bool) bool
	walk = func(j, prev int, jumped bool) bool {
		if j == len(query) {
			return jumped
		}
		key := state{j: j, prev: prev, jumped: jumped}
		if failed[key] {
			return false
		}
		want := query[j]
		wordEnd := nextStart[prev]
		if prev+1 < wordEnd && folded[prev+1] == want {
			positions[j] = prev + 1
			if walk(j+1, prev+1, jumped) {
				return true
			}
		}
		for at := wordEnd; at < len(folded); at = nextStart[at] {
			if folded[at] == want {
				positions[j] = at
				if walk(j+1, at, true) {
					return true
				}
			}
		}
		for at := prev + 2; at < wordEnd; at++ {
			if folded[at] == want {
				positions[j] = at
				if walk(j+1, at, jumped) {
					return true
				}
			}
		}
		failed[key] = true
		return false
	}
	for at := range folded {
		if starts[at] && folded[at] == query[0] {
			positions[0] = at
			if walk(1, at, false) {
				return positions
			}
		}
	}
	return nil
}

func isRuneSubsequence(query, name []rune) bool {
	j := 0
	for _, r := range name {
		if j < len(query) && r == query[j] {
			j++
		}
	}
	return j == len(query)
}

func rangesOfPositions(positions []int) []SourceTextRange {
	out := make([]SourceTextRange, 0, len(positions))
	for _, at := range positions {
		if n := len(out); n > 0 && out[n-1].End == at {
			out[n-1].End = at + 1
			continue
		}
		out = append(out, SourceTextRange{Start: at, End: at + 1})
	}
	return out
}

// isSymbolTokenRune reports runes of an identifier token in a content line.
func isSymbolTokenRune(r rune) bool {
	return isSymbolAlnum(r) || r == '_' || r == '$'
}

// bestTokenTier is the best tier any identifier token in text reaches.
func (m symbolNameMatcher) bestTokenTier(text string) symbolMatchTier {
	best := symbolTierNone
	for _, token := range strings.FieldsFunc(text, func(r rune) bool { return !isSymbolTokenRune(r) }) {
		if tier, _ := m.match(token); tier < best {
			best = tier
			if best == symbolTierExactCase {
				break
			}
		}
	}
	return best
}

// symbolHumpPattern matches, case-sensitively, a superset of the lines holding an
// identifier the query abbreviates; the name matcher decides. Only bounded letter
// and digit queries have one.
func symbolHumpPattern(query string) (string, bool) {
	runes := []rune(query)
	if len(runes) < 2 || len(runes) > symbolHumpPatternLimit {
		return "", false
	}
	for _, r := range runes {
		if !isSymbolAlnum(r) {
			return "", false
		}
	}
	const gap = `[\p{L}\p{N}_$]*`
	anyCase := func(r rune) string {
		lower, upper := unicode.ToLower(r), unicode.ToUpper(r)
		if lower == upper {
			return regexp.QuoteMeta(string(r))
		}
		return "[" + string(lower) + string(upper) + "]"
	}
	// The first rune may follow any non-letter; later word starts stay inside
	// one identifier token, after an underscore or dollar sign.
	wordStart := func(r rune, first bool) string {
		if unicode.IsDigit(r) {
			before := `(?:[_$]|\p{L})`
			if first {
				before = `(?:^|[^\p{N}])`
			}
			return "(?:" + before + regexp.QuoteMeta(string(r)) + ")"
		}
		before := `(?:[_$]|\p{N})`
		if first {
			before = `(?:^|[^\p{L}])`
		}
		start := before + anyCase(r)
		if upper := unicode.ToUpper(r); upper != unicode.ToLower(r) {
			start += "|" + string(upper)
		}
		return "(?:" + start + ")"
	}
	tail := func(from int) string {
		var b strings.Builder
		for _, r := range runes[from:] {
			b.WriteString(gap)
			b.WriteString(anyCase(r))
		}
		return b.String()
	}
	// rest matches runes[k:] with at least one of them beginning a word.
	var rest func(k int) string
	rest = func(k int) string {
		if k == len(runes)-1 {
			return gap + wordStart(runes[k], false)
		}
		return "(?:" + gap + wordStart(runes[k], false) + tail(k+1) + "|" + gap + anyCase(runes[k]) + rest(k+1) + ")"
	}
	return wordStart(runes[0], true) + rest(1), true
}
