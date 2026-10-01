//go:build !grammar_subset || grammar_subset_scheme || grammar_subset_clojure

package grammars

import (
	"fmt"
	"testing"

	"github.com/odvcencio/gotreesitter"
)

// assertIncrementalMatchesFreshFullSpan compares an incrementally parsed tree
// against a from-scratch parse of the same source using BOTH an SExpr
// structural comparison AND a full per-node recursive comparison of symbol,
// byte span, error flag, and child count.
//
// SExpr-string equality alone is not sufficient: it can miss divergences
// where a node's symbol and rendered text happen to match but its byte span
// does not (e.g. a stale reused subtree that keeps its old, pre-edit
// boundaries while a sibling absorbs or loses the corresponding bytes). See
// the d12b non-leaf reuse boundary guard in incremental.go, which this helper
// backs regression coverage for.
func assertIncrementalMatchesFreshFullSpan(t *testing.T, lang *gotreesitter.Language, incTree, freshTree *gotreesitter.Tree) {
	t.Helper()

	incStr := incTree.RootNode().SExpr(lang)
	freshStr := freshTree.RootNode().SExpr(lang)
	if incStr != freshStr {
		t.Errorf("DIVERGENCE (structure):\nincremental: %s\nfresh:       %s", incStr, freshStr)
	}

	if diff := d12bNodeSpanDiff(incTree.RootNode(), freshTree.RootNode()); diff != "" {
		t.Errorf("DIVERGENCE (per-node span): %s\nincremental: %s\nfresh:       %s", diff, incStr, freshStr)
	}
}

// d12bNodeSpanDiff performs a full recursive structural comparison (symbol,
// byte span, error flag, child count) between two node trees and returns a
// description of the first difference found, or "" if they are identical.
func d12bNodeSpanDiff(a, b *gotreesitter.Node) string {
	if a == nil && b == nil {
		return ""
	}
	if a == nil || b == nil {
		return fmt.Sprintf("nil mismatch: incremental-nil=%v fresh-nil=%v", a == nil, b == nil)
	}
	if a.Symbol() != b.Symbol() {
		return fmt.Sprintf("symbol mismatch: incremental=%d [%d,%d) fresh=%d [%d,%d)",
			a.Symbol(), a.StartByte(), a.EndByte(), b.Symbol(), b.StartByte(), b.EndByte())
	}
	if a.StartByte() != b.StartByte() || a.EndByte() != b.EndByte() {
		return fmt.Sprintf("span mismatch for symbol %d: incremental=[%d,%d) fresh=[%d,%d)",
			a.Symbol(), a.StartByte(), a.EndByte(), b.StartByte(), b.EndByte())
	}
	if a.HasError() != b.HasError() {
		return fmt.Sprintf("hasError mismatch for symbol %d at [%d,%d): incremental=%v fresh=%v",
			a.Symbol(), a.StartByte(), a.EndByte(), a.HasError(), b.HasError())
	}
	if a.ChildCount() != b.ChildCount() {
		return fmt.Sprintf("childCount mismatch for symbol %d at [%d,%d): incremental=%d fresh=%d",
			a.Symbol(), a.StartByte(), a.EndByte(), a.ChildCount(), b.ChildCount())
	}
	for i := 0; i < a.ChildCount(); i++ {
		if diff := d12bNodeSpanDiff(a.Child(i), b.Child(i)); diff != "" {
			return diff
		}
	}
	return ""
}
