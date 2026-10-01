package grammars

import (
	"testing"

	ts "github.com/odvcencio/gotreesitter"
)

// TestSwiftBareControlTransferKeywordChild is the proof test for the bare
// keyword case (childCount=0, span covers exactly the keyword) previously
// patched by the now-removed normalizeCollapsedNamedLeafChildrenBySource(...,
// "control_transfer_statement", "return", "continue", "break", "yield") call
// inside normalizeSwiftCompatibility. It asserts, on a real parse, that
// control_transfer_statement retains its anonymous keyword token child for a
// bare `return` with no result expression.
//
// grammargen's reduce path drops this leading keyword through the hidden
// _optionally_valueful_control_keyword rule, so this case was suspected of
// possibly needing the patch even though the other different-named-anonymous
// wrappers removed in this batch are covered by
// shouldKeepVisibleAnonymousTokenChild's invariant. Verified empirically:
// this test passes both with and without the patch, confirming the engine
// preserves the keyword child natively even through the hidden reduction, so
// the patch was dead here too.
func TestSwiftBareControlTransferKeywordChild(t *testing.T) {
	lang := SwiftLanguage()
	parser := ts.NewParser(lang)
	src := []byte("func f() {\n  return\n}\n")
	tree, err := parser.Parse(src)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	root := tree.RootNode()
	if root == nil {
		t.Fatal("missing root node")
	}
	if tree.ParseStopReason() != ts.ParseStopAccepted {
		t.Fatalf("stop=%s runtime=%s", tree.ParseStopReason(), tree.ParseRuntime().Summary())
	}
	if root.HasError() {
		t.Fatalf("expected bare `return` to parse cleanly, got %s", root.SExpr(lang))
	}
	stmt := findFirstNamedDescendantWhere(root, lang, "control_transfer_statement", func(*ts.Node) bool { return true })
	if stmt == nil {
		t.Fatalf("missing control_transfer_statement node; tree=%s", root.SExpr(lang))
	}
	if got := stmt.ChildCount(); got != 1 {
		t.Fatalf("control_transfer_statement child count = %d, want 1; tree=%s", got, root.SExpr(lang))
	}
	child := stmt.Child(0)
	if child == nil {
		t.Fatalf("control_transfer_statement missing return child; node=%s", stmt.SExpr(lang))
	}
	if child.Type(lang) != "return" || child.IsNamed() {
		t.Fatalf("control_transfer_statement child type/named = %q/%v, want return/false; node=%s", child.Type(lang), child.IsNamed(), stmt.SExpr(lang))
	}
	if child.StartByte() != stmt.StartByte() || child.EndByte() != stmt.EndByte() {
		t.Fatalf("return child byte range = [%d,%d), want [%d,%d) to match parent", child.StartByte(), child.EndByte(), stmt.StartByte(), stmt.EndByte())
	}
}
