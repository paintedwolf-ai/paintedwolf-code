//go:build !grammar_subset

package grammars

import (
	"testing"

	gotreesitter "github.com/odvcencio/gotreesitter"
)

func TestObjcStructSizedTypeSpecifierNeedsNoResultCompatibility(t *testing.T) {
	source := []byte("@interface Box : NSObject\n{\n  unsigned long _version;\n}\n@end\n")
	language := ObjcLanguage()
	parser := gotreesitter.NewParser(language)
	tree, err := parser.ParseNoResultCompatibilityBenchmarkOnly(source)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(tree.Release)

	declaration := firstObjcStructNodeByType(tree.RootNode(), language, "struct_declaration")
	if declaration == nil {
		t.Fatalf("missing struct_declaration: %s", tree.RootNode().SExpr(language))
	}
	if got, want := declaration.ChildCount(), 3; got != want {
		t.Fatalf("struct_declaration child count = %d, want %d: %s",
			got, want, tree.RootNode().SExpr(language))
	}
	sized := declaration.Child(0)
	if got, want := sized.Type(language), "sized_type_specifier"; got != want {
		t.Fatalf("child 0 = %q, want %q: %s", got, want, tree.RootNode().SExpr(language))
	}
	if got, want := sized.ChildCount(), 2; got != want {
		t.Fatalf("sized_type_specifier child count = %d, want %d: %s",
			got, want, tree.RootNode().SExpr(language))
	}
}

func firstObjcStructNodeByType(
	node *gotreesitter.Node,
	language *gotreesitter.Language,
	typ string,
) *gotreesitter.Node {
	if node == nil {
		return nil
	}
	if node.Type(language) == typ {
		return node
	}
	for i := 0; i < node.ChildCount(); i++ {
		if found := firstObjcStructNodeByType(node.Child(i), language, typ); found != nil {
			return found
		}
	}
	return nil
}
