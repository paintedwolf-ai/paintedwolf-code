//go:build !grammar_subset || grammar_subset_clojure

package grammars

import (
	"testing"

	"github.com/odvcencio/gotreesitter"
)

// TestD12bClojureAbuttingNonLeafBoundary is the non-leaf counterpart of the
// Scheme leaf-boundary repro in scheme_incremental_abutting_leaf_boundary_test.go.
//
// PR #360 rejected stale LEAF reuse when a fresh lex at the candidate's start
// byte produces a token with a different end byte (incremental.go,
// reuseTargetState leaf branch). That guard does not cover the parallel
// non-leaf fallback path: reuseNonLeafTargetStateOnStack (called from the
// "conservative fallback" loop in tryReuseSubtree) reuses a non-terminal
// purely via lookupGoto(preGoto, n.Symbol()) plus stack-depth matching -- it
// never consults the lookahead token at all, so it has no symbol check and no
// end-byte check.
//
// Clojure's grammar has a thin single-token wrapper non-terminal
// (sym_lit -> sym_name) that exposes exactly this gap: deleting the space in
// "(a b)" makes "ab" a single symbol under a fresh parse, but the stale
// sym_lit wrapping the old "a" leaf gets reused by the non-leaf fallback path
// (its own goto/stack-depth checks are satisfied), leaving the parser to
// resume lexing at the old leaf's stale end byte and re-split "ab" into two
// tokens, producing an ERROR node that a fresh parse does not have.
//
// Each case here deletes a byte that makes two previously separate tokens
// abut (or, for InsertReverse, does the opposite edit), and asserts full
// per-node byte-span equality between the incremental and fresh trees -- not
// just SExpr string equality, since the leftmost leaf's rendered text can
// still equal the fresh token's text even when spans/symbols differ
// underneath a reused wrapper.
func TestD12bClojureAbuttingNonLeafBoundary(t *testing.T) {
	cases := []struct {
		name       string
		oldSrc     string
		newSrc     string
		startByte  uint32
		oldEndByte uint32
		newEndByte uint32
	}{
		{
			name:       "paren_sym_delete_space",
			oldSrc:     "(a b)",
			newSrc:     "(ab)",
			startByte:  2,
			oldEndByte: 3,
			newEndByte: 2,
		},
		{
			name:       "bracket_sym_delete_space",
			oldSrc:     "[a b]",
			newSrc:     "[ab]",
			startByte:  2,
			oldEndByte: 3,
			newEndByte: 2,
		},
		{
			name:       "brace_sym_delete_space",
			oldSrc:     "{a b}",
			newSrc:     "{ab}",
			startByte:  2,
			oldEndByte: 3,
			newEndByte: 2,
		},
		{
			name:       "paren_word_delete_space",
			oldSrc:     "(foo bar)",
			newSrc:     "(foobar)",
			startByte:  4,
			oldEndByte: 5,
			newEndByte: 4,
		},
		{
			name:       "top_level_delete_space",
			oldSrc:     "a b c",
			newSrc:     "ab c",
			startByte:  1,
			oldEndByte: 2,
			newEndByte: 1,
		},
		{
			name:       "trailing_newline_delete_space",
			oldSrc:     "a b\n",
			newSrc:     "ab\n",
			startByte:  1,
			oldEndByte: 2,
			newEndByte: 1,
		},
	}

	lang := ClojureLanguage()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			oldSrc := []byte(tc.oldSrc)
			newSrc := []byte(tc.newSrc)

			parser := gotreesitter.NewParser(lang)
			oldTree, err := parser.Parse(oldSrc)
			if err != nil {
				t.Fatalf("parse old failed: %v", err)
			}
			t.Logf("OLD TREE:\n%s", oldTree.RootNode().SExpr(lang))

			oldTree.Edit(gotreesitter.InputEdit{
				StartByte:   tc.startByte,
				OldEndByte:  tc.oldEndByte,
				NewEndByte:  tc.newEndByte,
				StartPoint:  gotreesitter.Point{Row: 0, Column: tc.startByte},
				OldEndPoint: gotreesitter.Point{Row: 0, Column: tc.oldEndByte},
				NewEndPoint: gotreesitter.Point{Row: 0, Column: tc.newEndByte},
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

			// Full per-node byte-span check across the whole tree, not just
			// SExpr structural equality.
			assertIncrementalMatchesFreshFullSpan(t, lang, incTree, freshTree)

			if incTree.RootNode().HasError() {
				t.Errorf("incremental tree has error, fresh does not:\nincremental: %s\nfresh:       %s", incStr, freshStr)
			}
		})
	}
}

// TestD12bClojureAbuttingNonLeafBoundaryInsertReverse is the reverse-edit
// mirror of TestD12bClojureAbuttingNonLeafBoundary: starting from the merged
// form and inserting a byte that splits one token into two. This exercises
// the non-leaf fallback path from the opposite direction (growing a
// previously-single-token span back into a wrapper + two children).
func TestD12bClojureAbuttingNonLeafBoundaryInsertReverse(t *testing.T) {
	lang := ClojureLanguage()

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

	assertIncrementalMatchesFreshFullSpan(t, lang, incTree, freshTree)
}
