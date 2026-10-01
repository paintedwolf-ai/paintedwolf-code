package grammars

import (
	"testing"

	ts "github.com/odvcencio/gotreesitter"
)

// TestElixirBooleanKeepsTrueTokenChildViaEngine proves that the reduce engine
// restores the anonymous `true` token child of `boolean` on a real parse,
// without help from the (now removed)
// normalizeCollapsedNamedLeafChildrenBySource(..., "boolean", "true",
// "false") call inside normalizeElixirCollapsedLiteralChildren.
// shouldKeepVisibleAnonymousTokenChild keeps different-named
// single-token-wrapper anonymous children unconditionally, so `boolean`
// (named) wrapping `true`/`false` (anonymous) is never collapsed to a
// childless leaf in the first place.
func TestElixirBooleanKeepsTrueTokenChildViaEngine(t *testing.T) {
	lang := ElixirLanguage()
	parser := ts.NewParser(lang)
	src := []byte("x = true\n")
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
		t.Fatalf("expected `x = true` to parse cleanly, got %s", root.SExpr(lang))
	}
	boolean := findFirstNamedDescendantWhere(root, lang, "boolean", func(*ts.Node) bool { return true })
	if boolean == nil {
		t.Fatalf("missing boolean node; tree=%s", root.SExpr(lang))
	}
	if got := boolean.ChildCount(); got != 1 {
		t.Fatalf("boolean child count = %d, want 1; tree=%s", got, root.SExpr(lang))
	}
	child := boolean.Child(0)
	if child == nil {
		t.Fatalf("boolean missing true child; node=%s", boolean.SExpr(lang))
	}
	if child.Type(lang) != "true" || child.IsNamed() {
		t.Fatalf("boolean child type/named = %q/%v, want true/false; node=%s", child.Type(lang), child.IsNamed(), boolean.SExpr(lang))
	}
	if child.StartByte() != boolean.StartByte() || child.EndByte() != boolean.EndByte() {
		t.Fatalf("true child byte range = [%d,%d), want [%d,%d) to match parent", child.StartByte(), child.EndByte(), boolean.StartByte(), boolean.EndByte())
	}
}
