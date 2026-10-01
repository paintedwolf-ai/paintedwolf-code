package search

import (
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestDefaultSearchMatcherUsesCaseInsensitiveSubstring(t *testing.T) {
	m, err := compileSearchTermMatcher("Toolbar", false, MatchFlags{})
	testutil.FailErr(t, "compile search matcher", err)
	if !m.matches("the toolbar front") {
		t.Fatal("expected case-insensitive substring match")
	}
	if m.matches("tool") {
		t.Fatal("partial term should not match as whole needle when needle is Toolbar")
	}
}

func TestRegexMatcherCaseAndWord(t *testing.T) {
	m, err := compileReplaceMatcher(TextExpr{Text: `foo(\d+)`}, MatchFlags{Regex: true})
	testutil.FailErr(t, "compile replace matcher", err)
	if !m.matches("Foo42") {
		t.Fatal("expected FoldCase regex match")
	}
	line := "x Foo42 y"
	spans := m.findAll(line, "N$1")
	if len(spans) != 1 {
		t.Fatalf("spans = %v", spans)
	}
	frag := spans[0].fragment
	if frag != "N42" {
		t.Fatalf("expand = %q", frag)
	}

	wm, err := compileSearchTermMatcher("foo", false, MatchFlags{WholeWord: true, CaseSensitive: true})
	testutil.FailErr(t, "compile whole-word search matcher", err)
	if wm.matches("foobar") {
		t.Fatal("whole-word should reject foobar")
	}
	if !wm.matches("a foo b") {
		t.Fatal("whole-word should match foo")
	}
}

func TestInvalidRegexMatchError(t *testing.T) {
	_, err := compileSearchTermMatcher(`(`, false, MatchFlags{Regex: true})
	if err == nil {
		t.Fatal("expected error")
	}
	var me *MatchError
	if !errors.As(err, &me) {
		t.Fatalf("want MatchError, got %T", err)
	}
}

func TestCaseInsensitiveReplaceMatcherPreservesUnicodeByteRanges(t *testing.T) {
	m, err := compileReplaceMatcher(TextExpr{Text: "k"}, MatchFlags{})
	testutil.FailErr(t, "compile Unicode replace matcher", err)
	line := "temperature in \u212a"
	spans := m.findAll(line, "K")
	if len(spans) != 1 {
		t.Fatalf("spans = %v, want one Kelvin-sign match", spans)
	}
	if frag := spans[0].fragment; frag != "K" {
		t.Fatalf("expand = %q", frag)
	}
}
