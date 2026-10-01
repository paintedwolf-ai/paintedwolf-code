package grammars

import (
	"testing"

	ts "github.com/odvcencio/gotreesitter"
)

// TestPHPMethodModifiersKeepTokenChildrenViaEngine proves that the reduce
// engine restores the anonymous token children of PHP's
// static_modifier/abstract_modifier/final_modifier/readonly_modifier/
// visibility_modifier wrapper nodes on a real parse, without help from the
// (now removed) php normalizePHPCollapsedModifierChildren post-hoc patch.
// shouldKeepVisibleAnonymousTokenChild keeps different-named
// single-token-wrapper anonymous children unconditionally, so these named
// wrappers around anonymous keyword tokens are never collapsed to childless
// leaves in the first place.
func TestPHPMethodModifiersKeepTokenChildrenViaEngine(t *testing.T) {
	lang := PhpLanguage()
	parser := ts.NewParser(lang)
	src := []byte("<?php\nclass C {\n  public function f() {}\n  abstract static final readonly public function g();\n}\n")
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
		t.Fatalf("expected method modifiers to parse cleanly, got %s", root.SExpr(lang))
	}

	cases := []struct {
		wrapperType string
		childType   string
	}{
		{"visibility_modifier", "public"},
		{"static_modifier", "static"},
		{"abstract_modifier", "abstract"},
		{"final_modifier", "final"},
		{"readonly_modifier", "readonly"},
	}
	for _, tc := range cases {
		wrapper := findFirstNamedDescendantWhere(root, lang, tc.wrapperType, func(*ts.Node) bool { return true })
		if wrapper == nil {
			t.Fatalf("missing %s node; tree=%s", tc.wrapperType, root.SExpr(lang))
		}
		if got := wrapper.ChildCount(); got != 1 {
			t.Fatalf("%s child count = %d, want 1; tree=%s", tc.wrapperType, got, root.SExpr(lang))
		}
		child := wrapper.Child(0)
		if child == nil {
			t.Fatalf("%s missing token child; node=%s", tc.wrapperType, wrapper.SExpr(lang))
		}
		if child.Type(lang) != tc.childType || child.IsNamed() {
			t.Fatalf("%s child type/named = %q/%v, want %s/false; node=%s", tc.wrapperType, child.Type(lang), child.IsNamed(), tc.childType, wrapper.SExpr(lang))
		}
	}
}
