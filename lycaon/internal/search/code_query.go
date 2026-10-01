package search

import (
	"fmt"
	"strings"
)

type codeCandidate struct {
	kind string
	text string
	path string
}

type codeQueryMatcher interface {
	matches(codeCandidate) bool
}

type codeTextMatcher struct {
	matcher textMatcher
}

func (m codeTextMatcher) matches(candidate codeCandidate) bool {
	return m.matcher.matches(candidate.text)
}

type codeFilterMatcher struct {
	field string
	value string
}

func (m codeFilterMatcher) matches(candidate codeCandidate) bool {
	switch m.field {
	case "project":
		return true
	case "path":
		return pathFilterMatch(m.value, candidate.path)
	case "kind":
		return strings.EqualFold(m.value, candidate.kind)
	default:
		return false
	}
}

type codeAndMatcher struct {
	children []codeQueryMatcher
}

func (m codeAndMatcher) matches(candidate codeCandidate) bool {
	for _, child := range m.children {
		if !child.matches(candidate) {
			return false
		}
	}
	return true
}

type codeOrMatcher struct {
	children []codeQueryMatcher
}

func (m codeOrMatcher) matches(candidate codeCandidate) bool {
	for _, child := range m.children {
		if child.matches(candidate) {
			return true
		}
	}
	return false
}

type codeNotMatcher struct {
	child codeQueryMatcher
}

func (m codeNotMatcher) matches(candidate codeCandidate) bool {
	return !m.child.matches(candidate)
}

func compileCodeQuery(n Node, flags MatchFlags) (codeQueryMatcher, error) {
	switch v := n.(type) {
	case AndExpr:
		if len(v.Exprs) == 0 {
			return nil, &MatchError{Message: "AND requires an expression"}
		}
		children := make([]codeQueryMatcher, 0, len(v.Exprs))
		for _, child := range v.Exprs {
			matcher, err := compileCodeQuery(child, flags)
			if err != nil {
				return nil, err
			}
			children = append(children, matcher)
		}
		return codeAndMatcher{children: children}, nil
	case OrExpr:
		if len(v.Exprs) == 0 {
			return nil, &MatchError{Message: "OR requires an expression"}
		}
		children := make([]codeQueryMatcher, 0, len(v.Exprs))
		for _, child := range v.Exprs {
			matcher, err := compileCodeQuery(child, flags)
			if err != nil {
				return nil, err
			}
			children = append(children, matcher)
		}
		return codeOrMatcher{children: children}, nil
	case NotExpr:
		matcher, err := compileCodeQuery(v.Expr, flags)
		if err != nil {
			return nil, err
		}
		return codeNotMatcher{child: matcher}, nil
	case FilterExpr:
		return codeFilterMatcher{
			field: strings.ToLower(strings.TrimSpace(v.Field)),
			value: strings.TrimSpace(v.Value),
		}, nil
	case TextExpr:
		matcher, err := compileSearchTermMatcher(v.Text, v.Phrase, flags)
		if err != nil {
			return nil, ensureMatchError(err)
		}
		return codeTextMatcher{matcher: matcher}, nil
	default:
		return nil, &MatchError{Message: "invalid code query expression"}
	}
}

func queryCanMatchCandidate(n Node, candidateKind string) bool {
	switch v := n.(type) {
	case AndExpr:
		if len(v.Exprs) == 0 {
			return false
		}
		for _, child := range v.Exprs {
			if !queryCanMatchCandidate(child, candidateKind) {
				return false
			}
		}
		return true
	case OrExpr:
		for _, child := range v.Exprs {
			if queryCanMatchCandidate(child, candidateKind) {
				return true
			}
		}
		return false
	case NotExpr:
		// Excluding this arm's own kind rules the arm out; other negated
		// filters depend on per-candidate values, so the arm stays.
		if filter, ok := v.Expr.(FilterExpr); ok && strings.EqualFold(strings.TrimSpace(filter.Field), "kind") {
			return !strings.EqualFold(strings.TrimSpace(filter.Value), candidateKind)
		}
		return true
	case TextExpr:
		return true
	case FilterExpr:
		field := strings.ToLower(strings.TrimSpace(v.Field))
		value := strings.ToLower(strings.TrimSpace(v.Value))
		if field == "project" || field == "path" {
			return true
		}
		if field == "kind" {
			switch candidateKind {
			case ExecutorStore:
				return !isLiveSourceKind(value)
			case HitKindCode, HitKindFile, HitKindSymbol:
				return value == candidateKind
			}
		}
		return candidateKind == ExecutorStore
	default:
		return false
	}
}

func queryTextTerms(n Node) []string {
	switch v := n.(type) {
	case AndExpr:
		var out []string
		for _, child := range v.Exprs {
			out = append(out, queryTextTerms(child)...)
		}
		return out
	case OrExpr:
		var out []string
		for _, child := range v.Exprs {
			out = append(out, queryTextTerms(child)...)
		}
		return out
	case TextExpr:
		return searchTermsFor(v.Text, v.Phrase)
	default:
		// Excluded terms do not score or highlight.
		return nil
	}
}

// replacementPattern returns the single text expression used by search-replace.
// Filters may narrow replacement scope, but Boolean and multi-text replacement
// expressions are ambiguous and rejected.
func replacementPattern(n Node) (TextExpr, error) {
	pattern, found, err := replacementText(n)
	if err != nil {
		return TextExpr{}, err
	}
	if !found {
		return TextExpr{}, &MatchError{Message: "search-replace requires exactly one text expression"}
	}
	return pattern, nil
}

func replacementText(n Node) (TextExpr, bool, error) {
	switch v := n.(type) {
	case AndExpr:
		var pattern TextExpr
		found := false
		for _, child := range v.Exprs {
			childPattern, childFound, err := replacementText(child)
			if err != nil {
				return TextExpr{}, false, err
			}
			if !childFound {
				continue
			}
			if found {
				return TextExpr{}, false, &MatchError{Message: "search-replace requires exactly one text expression"}
			}
			pattern = childPattern
			found = true
		}
		return pattern, found, nil
	case OrExpr, NotExpr:
		return TextExpr{}, false, &MatchError{Message: "search-replace requires a conjunctive query"}
	case FilterExpr:
		return TextExpr{}, false, nil
	case TextExpr:
		return v, true, nil
	default:
		return TextExpr{}, false, &MatchError{Message: fmt.Sprintf("invalid replace query expression %T", n)}
	}
}

func replacementPathAllowed(n Node, path string) bool {
	switch v := n.(type) {
	case AndExpr:
		for _, child := range v.Exprs {
			if !replacementPathAllowed(child, path) {
				return false
			}
		}
	case FilterExpr:
		if strings.EqualFold(v.Field, "path") {
			return pathFilterMatch(v.Value, path)
		}
	}
	return true
}
