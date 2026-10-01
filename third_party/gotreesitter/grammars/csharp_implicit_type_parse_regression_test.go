package grammars

import (
	"testing"

	ts "github.com/odvcencio/gotreesitter"
)

// TestCSharpImplicitTypeKeepsVarTokenChildViaEngine proves that the reduce
// engine restores the anonymous `var` token child of `implicit_type` on a
// real parse, without help from the (now removed) c_sharp
// resultCollapsedNamedLeafRules row. shouldKeepVisibleAnonymousTokenChild
// keeps different-named single-token-wrapper anonymous children
// unconditionally, so `implicit_type` (named) wrapping `var` (anonymous) is
// never collapsed to a childless leaf in the first place.
func TestCSharpImplicitTypeKeepsVarTokenChildViaEngine(t *testing.T) {
	lang := CSharpLanguage()
	parser := ts.NewParser(lang)
	src := []byte("class C { void M() { var x = 1; } }\n")
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
		t.Fatalf("expected implicit var declaration to parse cleanly, got %s", root.SExpr(lang))
	}
	implicitType := findFirstNamedDescendantWhere(root, lang, "implicit_type", func(*ts.Node) bool { return true })
	if implicitType == nil {
		t.Fatalf("missing implicit_type node; tree=%s", root.SExpr(lang))
	}
	if got := implicitType.ChildCount(); got != 1 {
		t.Fatalf("implicit_type child count = %d, want 1; tree=%s", got, root.SExpr(lang))
	}
	child := implicitType.Child(0)
	if child == nil {
		t.Fatalf("implicit_type missing var child; node=%s", implicitType.SExpr(lang))
	}
	if child.Type(lang) != "var" || child.IsNamed() {
		t.Fatalf("implicit_type child type/named = %q/%v, want var/false; node=%s", child.Type(lang), child.IsNamed(), implicitType.SExpr(lang))
	}
	if child.StartByte() != implicitType.StartByte() || child.EndByte() != implicitType.EndByte() {
		t.Fatalf("var child byte range = [%d,%d), want [%d,%d) to match parent", child.StartByte(), child.EndByte(), implicitType.StartByte(), implicitType.EndByte())
	}
}
