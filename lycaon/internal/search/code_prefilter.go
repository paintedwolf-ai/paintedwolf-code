package search

import (
	"bytes"
	"sort"
	"unicode/utf8"

	"github.com/lycaon/lycaon/internal/litprefilter"
)

// prefilterFold matches the query's case transform; ToLower and SimpleFold differ on 'ſ'.
type prefilterFold int

const (
	prefilterFoldNone prefilterFold = iota
	prefilterFoldLower
	prefilterFoldSimple
)

// codePrefilter is a required-literal gate run on file bytes before the line
// matcher. Inactive when the query has no required substring (OR, open regex,
// wildcard).
type codePrefilter struct {
	literals [][]byte
	fold     prefilterFold
}

func (p codePrefilter) active() bool {
	return len(p.literals) > 0
}

// literalStrings returns the required literals for the catalog literal index.
func (p codePrefilter) literalStrings() []string {
	out := make([]string, 0, len(p.literals))
	for _, lit := range p.literals {
		out = append(out, string(lit))
	}
	return out
}

func (p codePrefilter) matches(content []byte) bool {
	if !p.active() {
		return true
	}
	if p.fold != prefilterFoldLower {
		for _, lit := range p.literals {
			if !litprefilter.BufferHas(content, lit, p.fold == prefilterFoldSimple) {
				return false
			}
		}
		return true
	}
	// Non-ASCII misses need a lowercase copy; ASCII matches need no allocation.
	var lowered []byte
	contentASCII := -1
	for _, lit := range p.literals {
		if asciiOnlyBytes(lit) {
			if asciiContainsFold(content, lit) {
				continue
			}
			if contentASCII < 0 {
				if asciiOnlyBytes(content) {
					contentASCII = 1
				} else {
					contentASCII = 0
				}
			}
			if contentASCII == 1 {
				return false
			}
		}
		if lowered == nil {
			lowered = bytes.ToLower(content)
		}
		if !bytes.Contains(lowered, lit) {
			return false
		}
	}
	return true
}

func asciiOnlyBytes(b []byte) bool {
	for _, c := range b {
		if c >= utf8.RuneSelf {
			return false
		}
	}
	return true
}

func asciiByteEqualFold(a, b byte) bool {
	if a == b {
		return true
	}
	if 'A' <= a && a <= 'Z' {
		a += 'a' - 'A'
	}
	if 'A' <= b && b <= 'Z' {
		b += 'a' - 'A'
	}
	return a == b
}

// asciiContainsFold reports whether hay contains needle under ASCII case
// folding. Needle is pre-lowered; hay is arbitrary bytes.
func asciiContainsFold(hay, needle []byte) bool {
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

func compileCodePrefilter(n Node, flags MatchFlags) codePrefilter {
	prefilter := requiredLiterals(n, flags)
	// Long literals reject fastest; scan them first.
	sortLiteralsBySelectivity(prefilter.literals)
	return prefilter
}

func compileReplacePrefilter(term TextExpr, flags MatchFlags) codePrefilter {
	if term.Text == "*" && !term.Phrase && !flags.Regex {
		return codePrefilter{}
	}
	return regexpPrefilter(term.Text, flags)
}

func regexpPrefilter(pattern string, flags MatchFlags) codePrefilter {
	// The code prefilter requires every literal, so clauses with alternatives
	// stay out of it.
	var out codePrefilter
	for _, clause := range litprefilter.Extract(regexpExpression(pattern, flags), !flags.CaseSensitive).Clauses {
		if len(clause) != 1 {
			continue
		}
		out.literals = append(out.literals, clause[0].Bytes)
		if clause[0].Fold {
			out.fold = prefilterFoldSimple
		}
	}
	return out
}

func requiredLiterals(n Node, flags MatchFlags) codePrefilter {
	switch v := n.(type) {
	case AndExpr:
		var out codePrefilter
		for _, child := range v.Exprs {
			part := requiredLiterals(child, flags)
			out.literals = append(out.literals, part.literals...)
			out.fold = max(out.fold, part.fold)
		}
		return out
	case OrExpr, NotExpr, FilterExpr:
		return codePrefilter{}
	case TextExpr:
		return textRequiredLiterals(v.Text, v.Phrase, flags)
	default:
		return codePrefilter{}
	}
}

func textRequiredLiterals(pattern string, phrase bool, flags MatchFlags) codePrefilter {
	if pattern == "*" && !phrase && !flags.Regex {
		return codePrefilter{}
	}
	if flags.Regex || flags.WholeWord {
		return regexpPrefilter(pattern, flags)
	}
	terms := searchTermsFor(pattern, phrase)
	if !phrase && len(terms) == 1 && terms[0] == ".+" {
		return codePrefilter{}
	}
	out := make([][]byte, 0, len(terms))
	for _, term := range terms {
		lit := []byte(term)
		if !flags.CaseSensitive {
			lit = bytes.ToLower(lit)
		}
		if len(lit) == 0 || !utf8.Valid(lit) {
			continue
		}
		out = append(out, lit)
	}
	fold := prefilterFoldNone
	if !flags.CaseSensitive {
		fold = prefilterFoldLower
	}
	return codePrefilter{literals: out, fold: fold}
}

// sortLiteralsBySelectivity orders required literals longest-first, keeping
// the relative order of equal lengths stable.
func sortLiteralsBySelectivity(lits [][]byte) {
	sort.SliceStable(lits, func(i, j int) bool {
		return len(lits[i]) > len(lits[j])
	})
}
