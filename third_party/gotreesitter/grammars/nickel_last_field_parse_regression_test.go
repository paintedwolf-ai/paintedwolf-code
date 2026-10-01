package grammars

import (
	"testing"

	ts "github.com/odvcencio/gotreesitter"
)

// TestNickelLastFieldKeepsEllipsisTokenChildViaEngine proves that the reduce
// engine restores the anonymous `..` token child of `last_field` on a real
// parse, without help from a post-hoc AST patch (now removed).
// shouldKeepVisibleAnonymousTokenChild keeps different-named
// single-token-wrapper anonymous children unconditionally, so `last_field`
// (named) wrapping `..` (anonymous) is never collapsed to a childless leaf
// in the first place.
func TestNickelLastFieldKeepsEllipsisTokenChildViaEngine(t *testing.T) {
	lang := NickelLanguage()
	parser := ts.NewParser(lang)
	src := []byte("{ x = 1, .. }\n")
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
		t.Fatalf("expected open record with ellipsis to parse cleanly, got %s", root.SExpr(lang))
	}
	lastField := findFirstNamedDescendantWhere(root, lang, "last_field", func(*ts.Node) bool { return true })
	if lastField == nil {
		t.Fatalf("missing last_field node; tree=%s", root.SExpr(lang))
	}
	if got := lastField.ChildCount(); got != 1 {
		t.Fatalf("last_field child count = %d, want 1; tree=%s", got, root.SExpr(lang))
	}
	child := lastField.Child(0)
	if child == nil {
		t.Fatalf("last_field missing .. child; node=%s", lastField.SExpr(lang))
	}
	if child.Type(lang) != ".." || child.IsNamed() {
		t.Fatalf("last_field child type/named = %q/%v, want ../false; node=%s", child.Type(lang), child.IsNamed(), lastField.SExpr(lang))
	}
	if child.StartByte() != lastField.StartByte() || child.EndByte() != lastField.EndByte() {
		t.Fatalf(".. child byte range = [%d,%d), want [%d,%d) to match parent", child.StartByte(), child.EndByte(), lastField.StartByte(), lastField.EndByte())
	}
}
