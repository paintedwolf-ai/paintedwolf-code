package search

import (
	"errors"
	"fmt"
	"regexp"
	"regexp/syntax"
	"strings"
	"unicode/utf8"
)

// MatchFlags are additive search options.
type MatchFlags struct {
	Regex         bool
	CaseSensitive bool
	WholeWord     bool
	Include       []string
	Exclude       []string
}

// MatchError is a structured compile failure for a search/replace pattern.
type MatchError struct {
	Message string
}

func (e *MatchError) Error() string { return e.Message }

type textMatcher interface {
	matches(line string) bool
}

// replaceMatcher finds and replaces occurrences over whole file content.
type replaceMatcher interface {
	textMatcher
	// findAll returns ascending, non-overlapping matches with expanded fragments.
	findAll(content, replacement string) []replacementMatch
}

type replacementMatch struct {
	start, end int
	fragment   string
}

func compileSearchTermMatcher(pattern string, phrase bool, flags MatchFlags) (textMatcher, error) {
	if !phrase {
		pattern = strings.TrimSpace(pattern)
	}
	if pattern == "" || (pattern == "*" && !phrase && !flags.Regex) {
		return wildcardMatcher{}, nil
	}
	if !flags.Regex && !flags.CaseSensitive && !flags.WholeWord {
		return newSubstringMatcher(searchTermsFor(pattern, phrase), phrase), nil
	}
	if flags.Regex || flags.WholeWord {
		return compileRegexpMatcher(pattern, flags)
	}
	// Case-sensitive literal substring (no regex, no whole-word).
	return literalMatcher{needle: pattern}, nil
}

// searchTermsFor splits a bare term on whitespace; a quoted phrase stays whole.
func searchTermsFor(pattern string, phrase bool) []string {
	if phrase {
		return []string{pattern}
	}
	return codeSearchTerms(pattern)
}

func compileReplaceMatcher(term TextExpr, flags MatchFlags) (replaceMatcher, error) {
	pattern := term.Text
	if pattern == "" {
		return nil, &MatchError{Message: "pattern is required"}
	}
	if pattern == "*" && !term.Phrase && !flags.Regex {
		return wildcardMatcher{}, nil
	}
	if flags.Regex || flags.WholeWord {
		return compileRegexpMatcher(pattern, flags)
	}
	if flags.CaseSensitive {
		return literalMatcher{needle: pattern}, nil
	}
	return compileRegexpMatcher(pattern, flags)
}

type wildcardMatcher struct{}

func (wildcardMatcher) matches(line string) bool {
	return strings.TrimSpace(line) != ""
}

// findAll matches each non-empty line as one span.
func (wildcardMatcher) findAll(content, replacement string) []replacementMatch {
	var out []replacementMatch
	start := 0
	for start <= len(content) {
		lineEnd := len(content)
		if nl := strings.IndexByte(content[start:], '\n'); nl >= 0 {
			lineEnd = start + nl
		}
		if strings.TrimSpace(content[start:lineEnd]) != "" {
			out = append(out, replacementMatch{start: start, end: lineEnd, fragment: replacement})
		}
		if lineEnd == len(content) {
			break
		}
		start = lineEnd + 1
	}
	return out
}

type substringMatcher struct {
	terms []string
	// loweredTerms are folded once at compile; folding per line per term is
	// the hot path of a whole-corpus scan.
	loweredTerms []string
	// phrase marks a quoted term: ".+" is then the literal two characters,
	// not the match-any sentinel.
	phrase bool
}

func newSubstringMatcher(terms []string, phrase bool) substringMatcher {
	lowered := make([]string, len(terms))
	for i, term := range terms {
		lowered[i] = strings.ToLower(term)
	}
	return substringMatcher{terms: terms, loweredTerms: lowered, phrase: phrase}
}

func (m substringMatcher) matches(line string) bool {
	if len(m.terms) == 0 {
		return false
	}
	if !m.phrase && len(m.terms) == 1 && m.terms[0] == ".+" {
		return strings.TrimSpace(line) != ""
	}
	for _, term := range m.loweredTerms {
		if !containsFold(line, term) {
			return false
		}
	}
	return true
}

// containsFold matches lowered under strings.ToLower folding. An ASCII scan
// settles an ASCII line; a non-ASCII line is lowered, since İ lowers to i.
func containsFold(line, lowered string) bool {
	if asciiOnlyString(lowered) {
		if asciiContainsFoldString(line, lowered) {
			return true
		}
		if asciiOnlyString(line) {
			return false
		}
	}
	return strings.Contains(strings.ToLower(line), lowered)
}

func asciiOnlyString(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= utf8.RuneSelf {
			return false
		}
	}
	return true
}

// asciiContainsFoldString is asciiContainsFold over strings: needle is
// pre-lowered ASCII, hay is arbitrary bytes.
func asciiContainsFoldString(hay, needle string) bool {
	if len(needle) == 0 {
		return true
	}
	for i := 0; i+len(needle) <= len(hay); i++ {
		if !asciiByteEqualFold(hay[i], needle[0]) {
			continue
		}
		j := 1
		for ; j < len(needle); j++ {
			if !asciiByteEqualFold(hay[i+j], needle[j]) {
				break
			}
		}
		if j == len(needle) {
			return true
		}
	}
	return false
}

type literalMatcher struct {
	needle string
}

func (m literalMatcher) matches(line string) bool {
	return strings.Contains(line, m.needle)
}

// findAll scans the whole content; the needle may contain newlines.
func (m literalMatcher) findAll(content, replacement string) []replacementMatch {
	if m.needle == "" {
		return nil
	}
	var out []replacementMatch
	from := 0
	for {
		at := strings.Index(content[from:], m.needle)
		if at < 0 {
			break
		}
		start := from + at
		end := start + len(m.needle)
		out = append(out, replacementMatch{start: start, end: end, fragment: replacement})
		from = end
		if from >= len(content) {
			break
		}
	}
	return out
}

type regexpMatcher struct {
	re             *regexp.Regexp
	expandCaptures bool
}

func regexpExpression(pattern string, flags MatchFlags) string {
	expr := pattern
	if !flags.Regex {
		expr = regexp.QuoteMeta(pattern)
	}
	if flags.WholeWord {
		expr = `\b(?:` + expr + `)\b`
	}
	return expr
}

func compileRegexpMatcher(pattern string, flags MatchFlags) (regexpMatcher, error) {
	expr := regexpExpression(pattern, flags)
	// Multiline anchors match line boundaries.
	mode := syntax.Perl &^ syntax.OneLine
	if !flags.CaseSensitive {
		mode |= syntax.FoldCase
	}
	parsed, err := syntax.Parse(expr, mode)
	if err != nil {
		return regexpMatcher{}, &MatchError{Message: err.Error()}
	}
	re, err := regexp.Compile(parsed.String())
	if err != nil {
		return regexpMatcher{}, &MatchError{Message: err.Error()}
	}
	return regexpMatcher{re: re, expandCaptures: flags.Regex}, nil
}

func (m regexpMatcher) matches(line string) bool {
	return m.re.MatchString(line)
}

func (m regexpMatcher) findAll(content, replacement string) []replacementMatch {
	idxs := m.re.FindAllStringSubmatchIndex(content, -1)
	if len(idxs) == 0 {
		return nil
	}
	out := make([]replacementMatch, 0, len(idxs))
	for _, pair := range idxs {
		fragment := replacement
		if m.expandCaptures {
			fragment = string(m.re.ExpandString(nil, replacement, content, pair))
		}
		out = append(out, replacementMatch{start: pair[0], end: pair[1], fragment: fragment})
	}
	return out
}

// ensureMatchError wraps unknown compile failures.
func ensureMatchError(err error) error {
	if err == nil {
		return nil
	}
	var me *MatchError
	if errors.As(err, &me) {
		return err
	}
	return &MatchError{Message: fmt.Sprintf("%v", err)}
}
