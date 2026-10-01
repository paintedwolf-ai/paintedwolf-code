package search

import (
	"testing"
)

func TestCompileCodePrefilterSubstring(t *testing.T) {
	p := compileCodePrefilter(TextExpr{Text: "Needle"}, MatchFlags{})
	if !p.active() || len(p.literals) != 1 || string(p.literals[0]) != "needle" ||
		p.fold != prefilterFoldLower {
		t.Fatalf("prefilter = %+v", p)
	}
	if !p.matches([]byte("the NEEDLE here")) {
		t.Fatal("expected fold match")
	}
	if p.matches([]byte("nothing")) {
		t.Fatal("expected miss")
	}
}

func TestCompileCodePrefilterAndTerms(t *testing.T) {
	p := compileCodePrefilter(TextExpr{Text: "foo bar"}, MatchFlags{})
	if !p.active() || len(p.literals) != 2 {
		t.Fatalf("AND terms = %+v", p)
	}
	if p.matches([]byte("foo only")) {
		t.Fatal("missing bar must skip")
	}
	if !p.matches([]byte("foo and bar")) {
		t.Fatal("both terms present")
	}
}

func TestCompileCodePrefilterOrDisables(t *testing.T) {
	p := compileCodePrefilter(OrExpr{Exprs: []Node{
		TextExpr{Text: "alpha"},
		TextExpr{Text: "beta"},
	}}, MatchFlags{})
	if p.active() {
		t.Fatal("OR must not claim a required literal")
	}
}

func TestPrefilterFoldMatchesSubstringMatcherTransform(t *testing.T) {
	// The prefilter and substring matcher share lowercase folding.
	pf := compileCodePrefilter(TextExpr{Text: "İstanbul"}, MatchFlags{})
	if pf.fold != prefilterFoldLower {
		t.Fatalf("fold = %v, want lower", pf.fold)
	}
	if !pf.matches([]byte("visited İSTANBUL today")) {
		t.Fatal("prefilter dropped a matcher hit")
	}
}

func TestPrefilterRegexUsesSimpleFold(t *testing.T) {
	pf := compileCodePrefilter(TextExpr{Text: "status"}, MatchFlags{Regex: true})
	if !pf.active() {
		t.Fatal("expected an extracted literal")
	}
	if pf.fold != prefilterFoldSimple {
		t.Fatalf("fold = %v, want simple", pf.fold)
	}
	// 'ſ' (long s) folds to 's' under regex FoldCase but not under ToLower.
	if !pf.matches([]byte("ſtatuſ report")) {
		t.Fatal("prefilter dropped a regex-fold hit")
	}
}

func TestPrefilterLiteralsOrderLongestFirst(t *testing.T) {
	p := compileCodePrefilter(AndExpr{Exprs: []Node{
		TextExpr{Text: "ab"},
		TextExpr{Text: "abcdef"},
		TextExpr{Text: "abcd"},
	}}, MatchFlags{})
	lits := p.literals
	if len(lits) != 3 {
		t.Fatalf("literals = %v", lits)
	}
	if string(lits[0]) != "abcdef" || string(lits[2]) != "ab" {
		t.Fatalf("order = %q, %q, %q", lits[0], lits[1], lits[2])
	}
}

func TestPhraseLiteralStaysWhole(t *testing.T) {
	p := requiredLiterals(TextExpr{Text: "quick brown", Phrase: true}, MatchFlags{})
	if !p.active() || len(p.literals) != 1 || string(p.literals[0]) != "quick brown" {
		t.Fatalf("phrase prefilter = %+v", p)
	}
}

func TestPrefilterWholeWordWildcard(t *testing.T) {
	for _, sensitive := range []bool{false, true} {
		flags := MatchFlags{WholeWord: true, CaseSensitive: sensitive}
		p := compileCodePrefilter(TextExpr{Text: "*"}, flags)
		if p.active() || !p.matches([]byte("ordinary text")) {
			t.Fatalf("wildcard prefilter = %+v", p)
		}
		quoted := compileCodePrefilter(TextExpr{Text: "*", Phrase: true}, flags)
		if !quoted.active() || quoted.matches([]byte("ordinary text")) {
			t.Fatalf("quoted asterisk prefilter = %+v", quoted)
		}
	}
}
