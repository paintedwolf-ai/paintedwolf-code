package grammars

import (
	"testing"

	ts "github.com/odvcencio/gotreesitter"
)

// TestJavaModifiersKeepsTokenChildrenViaEngine proves that the reduce engine
// restores the anonymous `public`/`static` token children of `modifiers` on a
// real parse, without help from the (now removed) java
// normalizeJavaCollapsedLeafChildren post-hoc patch.
// shouldKeepVisibleAnonymousTokenChild keeps different-named
// single-token-wrapper anonymous children unconditionally, so `modifiers`
// (named) wrapping `public`/`static` (anonymous) is never collapsed to a
// childless leaf in the first place.
func TestJavaModifiersKeepsTokenChildrenViaEngine(t *testing.T) {
	lang := JavaLanguage()
	parser := ts.NewParser(lang)
	src := []byte("class C { public static int x; }\n")
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
		t.Fatalf("expected modifiers to parse cleanly, got %s", root.SExpr(lang))
	}
	modifiers := findFirstNamedDescendantWhere(root, lang, "modifiers", func(*ts.Node) bool { return true })
	if modifiers == nil {
		t.Fatalf("missing modifiers node; tree=%s", root.SExpr(lang))
	}
	if got := modifiers.ChildCount(); got != 2 {
		t.Fatalf("modifiers child count = %d, want 2; tree=%s", got, root.SExpr(lang))
	}
	wantTypes := []string{"public", "static"}
	for i, want := range wantTypes {
		child := modifiers.Child(i)
		if child == nil {
			t.Fatalf("modifiers missing child %d; node=%s", i, modifiers.SExpr(lang))
		}
		if child.Type(lang) != want || child.IsNamed() {
			t.Fatalf("modifiers child %d type/named = %q/%v, want %s/false; node=%s", i, child.Type(lang), child.IsNamed(), want, modifiers.SExpr(lang))
		}
	}
}

// TestJavaImportAsteriskKeepsTokenChildViaEngine proves that the reduce
// engine restores the anonymous `*` token child of `asterisk` on a real
// parse, without help from the (now removed) java
// normalizeJavaCollapsedLeafChildren post-hoc patch.
func TestJavaImportAsteriskKeepsTokenChildViaEngine(t *testing.T) {
	lang := JavaLanguage()
	parser := ts.NewParser(lang)
	src := []byte("import a.*;\n")
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
		t.Fatalf("expected wildcard import to parse cleanly, got %s", root.SExpr(lang))
	}
	asterisk := findFirstNamedDescendantWhere(root, lang, "asterisk", func(*ts.Node) bool { return true })
	if asterisk == nil {
		t.Fatalf("missing asterisk node; tree=%s", root.SExpr(lang))
	}
	if got := asterisk.ChildCount(); got != 1 {
		t.Fatalf("asterisk child count = %d, want 1; tree=%s", got, root.SExpr(lang))
	}
	child := asterisk.Child(0)
	if child == nil {
		t.Fatalf("asterisk missing * child; node=%s", asterisk.SExpr(lang))
	}
	if child.Type(lang) != "*" || child.IsNamed() {
		t.Fatalf("asterisk child type/named = %q/%v, want */false; node=%s", child.Type(lang), child.IsNamed(), asterisk.SExpr(lang))
	}
}
