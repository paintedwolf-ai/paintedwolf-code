package grammars

import (
	"testing"

	ts "github.com/odvcencio/gotreesitter"
)

// TestDartOperatorWrappersKeepTokenChildrenViaEngine proves that the reduce
// engine restores the anonymous token children of Dart's
// final_builtin/negation_operator/relational_operator wrapper nodes on real
// parses, without help from the (now removed) normalizeDartCollapsedLeafChildren
// calls for those symbols. shouldKeepVisibleAnonymousTokenChild keeps
// different-named single-token-wrapper anonymous children unconditionally, so
// these named wrappers around anonymous tokens are never collapsed to
// childless leaves in the first place.
func TestDartOperatorWrappersKeepTokenChildrenViaEngine(t *testing.T) {
	lang := DartLanguage()

	cases := []struct {
		name        string
		src         string
		wrapperType string
		childType   string
	}{
		{"final_builtin", "class C {\n  final x = 1;\n}\n", "final_builtin", "final"},
		{"negation_operator", "class C {\n  final b = !x;\n}\n", "negation_operator", "!"},
		{"relational_operator", "class C {\n  final b = 1 < 2;\n}\n", "relational_operator", "<"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			parser := ts.NewParser(lang)
			tree, err := parser.Parse([]byte(tc.src))
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
				t.Fatalf("expected %q to parse cleanly, got %s", tc.src, root.SExpr(lang))
			}
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
		})
	}
}
