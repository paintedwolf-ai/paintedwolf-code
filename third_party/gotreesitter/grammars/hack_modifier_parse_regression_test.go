package grammars

import (
	"testing"

	ts "github.com/odvcencio/gotreesitter"
)

// TestHackModifierWrappersKeepTokenChildrenViaEngine proves that the reduce
// engine restores the anonymous token children of Hack's
// async_modifier/abstract_modifier/static_modifier/variadic_modifier/
// visibility_modifier/scope_identifier wrapper nodes on real parses, without
// help from the (now removed) hack normalizeHackCompatibility calls for
// those symbols. shouldKeepVisibleAnonymousTokenChild keeps different-named
// single-token-wrapper anonymous children unconditionally, so these named
// wrappers around anonymous keyword tokens are never collapsed to childless
// leaves in the first place.
func TestHackModifierWrappersKeepTokenChildrenViaEngine(t *testing.T) {
	lang := HackLanguage()

	cases := []struct {
		name        string
		src         string
		wrapperType string
		childType   string
	}{
		{"visibility_modifier", "<?hh\nclass C {\n  public static async function f(): void {}\n}\n", "visibility_modifier", "public"},
		{"static_modifier", "<?hh\nclass C {\n  public static async function f(): void {}\n}\n", "static_modifier", "static"},
		{"async_modifier", "<?hh\nclass C {\n  public static async function f(): void {}\n}\n", "async_modifier", "async"},
		{"abstract_modifier", "<?hh\nabstract class C {}\n", "abstract_modifier", "abstract"},
		{"variadic_modifier", "<?hh\nfunction f(int... $args): void {}\n", "variadic_modifier", "..."},
		{"scope_identifier", "<?hh\nclass C {\n  public function f() { return parent::x(); }\n}\n", "scope_identifier", "parent"},
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
