// Package litprefilter extracts the literal substrings every match of a regular
// expression must contain, so a search can skip content that cannot match.
package litprefilter

import (
	"bytes"
	"cmp"
	"regexp/syntax"
	"slices"
	"unicode"
	"unicode/utf8"
)

// Literal is a substring with the case semantics of its regex node.
type Literal struct {
	Bytes []byte
	Fold  bool
}

// Clause holds literals of which every match contains at least one.
type Clause []Literal

// Requirement is what every match must contain: one literal from each clause.
// A requirement without clauses excludes no content.
type Requirement struct {
	Clauses []Clause
}

const (
	// maxClauseLiterals bounds the literals one clause checks per file; a wider
	// alternation is left unconstrained.
	maxClauseLiterals = 16
	// maxClauses keeps the most selective clauses of a long concatenation.
	maxClauses = 4
)

// AnyOf requires at least one of literals.
func AnyOf(literals ...string) Requirement {
	var clause Clause
	for _, text := range literals {
		if text != "" {
			clause = append(clause, Literal{Bytes: []byte(text)})
		}
	}
	if len(clause) == 0 {
		return Requirement{}
	}
	return Requirement{Clauses: []Clause{clause}}
}

// AllOf requires every one of literals.
func AllOf(literals ...string) Requirement {
	var out Requirement
	for _, text := range literals {
		if text != "" {
			out.Clauses = append(out.Clauses, Clause{{Bytes: []byte(text)}})
		}
	}
	return out
}

// Empty reports a requirement every content satisfies.
func (r Requirement) Empty() bool { return len(r.Clauses) == 0 }

// Matches reports whether content holds a literal from every clause.
func (r Requirement) Matches(content []byte) bool {
	for _, clause := range r.Clauses {
		if !clause.matches(content) {
			return false
		}
	}
	return true
}

func (c Clause) matches(content []byte) bool {
	for _, lit := range c {
		if BufferHas(content, lit.Bytes, lit.Fold) {
			return true
		}
	}
	return false
}

// Extract returns what every match of pattern must contain, or an empty
// requirement when the pattern is invalid or requires nothing.
func Extract(pattern string, caseInsensitive bool) Requirement {
	if pattern == "" {
		return Requirement{}
	}
	mode := syntax.Perl
	if caseInsensitive {
		mode |= syntax.FoldCase
	}
	parsed, err := syntax.Parse(pattern, mode)
	if err != nil {
		return Requirement{}
	}
	return required(parsed.Simplify())
}

// BufferHas reports whether haystack contains lit. When fold is true the
// comparison follows Unicode simple folding without changing the fold class.
func BufferHas(haystack, lit []byte, fold bool) bool {
	if len(lit) == 0 {
		return true
	}
	if !fold {
		return bytes.Contains(haystack, lit)
	}
	return containsFold(haystack, lit)
}

// required derives the clauses a node's matches share. A concatenation needs
// every part; an alternation needs one literal from some branch, so its clause
// joins each branch's most selective clause.
func required(re *syntax.Regexp) Requirement {
	switch re.Op {
	case syntax.OpLiteral:
		return single(Literal{Bytes: []byte(string(re.Rune)), Fold: re.Flags&syntax.FoldCase != 0})
	case syntax.OpCharClass:
		if len(re.Rune) == 2 && re.Rune[0] == re.Rune[1] {
			return single(Literal{Bytes: []byte(string(re.Rune[0]))})
		}
		return Requirement{}
	case syntax.OpConcat:
		var out Requirement
		for _, sub := range re.Sub {
			out.Clauses = append(out.Clauses, required(sub).Clauses...)
		}
		return mostSelective(out)
	case syntax.OpAlternate:
		var joined Clause
		for _, sub := range re.Sub {
			branch := required(sub)
			if branch.Empty() {
				return Requirement{}
			}
			joined = append(joined, branch.Clauses[0]...)
		}
		if len(joined) > maxClauseLiterals {
			return Requirement{}
		}
		return Requirement{Clauses: []Clause{joined}}
	case syntax.OpCapture, syntax.OpPlus:
		return required(re.Sub[0])
	case syntax.OpRepeat:
		if re.Min == 0 {
			return Requirement{}
		}
		return required(re.Sub[0])
	case syntax.OpStar, syntax.OpQuest,
		syntax.OpAnyChar, syntax.OpAnyCharNotNL, syntax.OpBeginLine, syntax.OpEndLine,
		syntax.OpBeginText, syntax.OpEndText, syntax.OpWordBoundary, syntax.OpNoWordBoundary:
		return Requirement{}
	default:
		if len(re.Sub) == 1 {
			return required(re.Sub[0])
		}
		return Requirement{}
	}
}

func single(lit Literal) Requirement {
	if !utf8.Valid(lit.Bytes) {
		return Requirement{}
	}
	return Requirement{Clauses: []Clause{{lit}}}
}

// mostSelective orders clauses by how much content they exclude and keeps the
// strongest. A clause is as selective as its shortest literal; fewer
// alternatives break ties.
func mostSelective(r Requirement) Requirement {
	slices.SortStableFunc(r.Clauses, func(a, b Clause) int {
		if c := cmp.Compare(b.shortest(), a.shortest()); c != 0 {
			return c
		}
		return cmp.Compare(len(a), len(b))
	})
	if len(r.Clauses) > maxClauses {
		r.Clauses = r.Clauses[:maxClauses]
	}
	return r
}

// shortest is the length of the clause's shortest literal.
func (c Clause) shortest() int {
	n := -1
	for _, lit := range c {
		if n < 0 || len(lit.Bytes) < n {
			n = len(lit.Bytes)
		}
	}
	return max(n, 0)
}

func containsFold(hay, needle []byte) bool {
	if len(needle) == 0 {
		return true
	}
	needleRunes := []rune(string(needle))
	for start := 0; start < len(hay); {
		cursor := start
		matched := true
		for _, want := range needleRunes {
			if cursor >= len(hay) {
				matched = false
				break
			}
			got, size := utf8.DecodeRune(hay[cursor:])
			if got == utf8.RuneError && size == 1 || !runesEqualFold(got, want) {
				matched = false
				break
			}
			cursor += size
		}
		if matched {
			return true
		}
		_, size := utf8.DecodeRune(hay[start:])
		start += size
	}
	return false
}

func runesEqualFold(a, b rune) bool {
	if a == b {
		return true
	}
	for folded := unicode.SimpleFold(a); folded != a; folded = unicode.SimpleFold(folded) {
		if folded == b {
			return true
		}
	}
	return false
}
