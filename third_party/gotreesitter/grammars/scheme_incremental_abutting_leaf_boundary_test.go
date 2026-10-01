//go:build !grammar_subset || grammar_subset_scheme

package grammars

import (
	"testing"

	"github.com/odvcencio/gotreesitter"
)

// TestD12bReproSchemeAbutDelete is the minimal incremental-correctness repro
// for the stale-leaf-reuse class: a leaf whose right edge abuts an edit start
// byte keeps a pre-edit token boundary and gets reused even though a fresh
// lex at the same start byte produces a token with a different end byte.
//
// (a b) --delete the space (byte 2)--> (ab)
//
// Before the reuseTargetState leaf-boundary fix, the stale leaf "a" [1,2) was
// reused, and the parser resumed lexing at byte 2, re-splitting "ab" into two
// symbols instead of lexing it as one (see incremental.go reuseTargetState).
func TestD12bReproSchemeAbutDelete(t *testing.T) {
	lang := SchemeLanguage()

	oldSrc := []byte("(a b)")
	newSrc := []byte("(ab)")

	parser := gotreesitter.NewParser(lang)
	oldTree, err := parser.Parse(oldSrc)
	if err != nil {
		t.Fatalf("parse old failed: %v", err)
	}
	t.Logf("OLD TREE:\n%s", oldTree.RootNode().SExpr(lang))

	oldTree.Edit(gotreesitter.InputEdit{
		StartByte:   2,
		OldEndByte:  3,
		NewEndByte:  2,
		StartPoint:  gotreesitter.Point{Row: 0, Column: 2},
		OldEndPoint: gotreesitter.Point{Row: 0, Column: 3},
		NewEndPoint: gotreesitter.Point{Row: 0, Column: 2},
	})

	incTree, err := parser.ParseIncremental(newSrc, oldTree)
	if err != nil {
		t.Fatalf("incremental parse failed: %v", err)
	}

	freshTree, err := gotreesitter.NewParser(lang).Parse(newSrc)
	if err != nil {
		t.Fatalf("fresh parse failed: %v", err)
	}

	incStr := incTree.RootNode().SExpr(lang)
	freshStr := freshTree.RootNode().SExpr(lang)

	t.Logf("INCREMENTAL:\n%s", incStr)
	t.Logf("FRESH:\n%s", freshStr)

	// Full per-node span check across the whole tree (not just the leaf
	// symbol checked below), catching divergences anywhere.
	assertIncrementalMatchesFreshFullSpan(t, lang, incTree, freshTree)

	incSym := incTree.RootNode().NamedChild(0).NamedChild(0)
	freshSym := freshTree.RootNode().NamedChild(0).NamedChild(0)
	if incSym.StartByte() != freshSym.StartByte() || incSym.EndByte() != freshSym.EndByte() {
		t.Errorf("span mismatch: incremental [%d,%d) fresh [%d,%d)", incSym.StartByte(), incSym.EndByte(), freshSym.StartByte(), freshSym.EndByte())
	}
	if incSym.Text(newSrc) != "ab" {
		t.Errorf("incremental symbol text = %q, want %q", incSym.Text(newSrc), "ab")
	}
}

func TestD12bReproSchemeAbutInsertReverse(t *testing.T) {
	lang := SchemeLanguage()

	oldSrc := []byte("(ab)")
	newSrc := []byte("(a b)")

	parser := gotreesitter.NewParser(lang)
	oldTree, err := parser.Parse(oldSrc)
	if err != nil {
		t.Fatalf("parse old failed: %v", err)
	}

	oldTree.Edit(gotreesitter.InputEdit{
		StartByte:   2,
		OldEndByte:  2,
		NewEndByte:  3,
		StartPoint:  gotreesitter.Point{Row: 0, Column: 2},
		OldEndPoint: gotreesitter.Point{Row: 0, Column: 2},
		NewEndPoint: gotreesitter.Point{Row: 0, Column: 3},
	})

	incTree, err := parser.ParseIncremental(newSrc, oldTree)
	if err != nil {
		t.Fatalf("incremental parse failed: %v", err)
	}
	freshTree, err := gotreesitter.NewParser(lang).Parse(newSrc)
	if err != nil {
		t.Fatalf("fresh parse failed: %v", err)
	}

	// Full per-node span check, not just SExpr structural equality: SExpr
	// string equality alone can miss a stale reused subtree that keeps its
	// pre-edit byte boundaries while rendering the same text.
	assertIncrementalMatchesFreshFullSpan(t, lang, incTree, freshTree)
}
