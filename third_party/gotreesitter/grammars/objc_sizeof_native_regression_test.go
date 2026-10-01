//go:build !grammar_subset

package grammars

import (
	"testing"

	gotreesitter "github.com/odvcencio/gotreesitter"
)

func TestObjcSizeofCustomTypeMatchesOracleWithoutResultCompatibility(t *testing.T) {
	source := []byte("void f(){int a=sizeof(CustomType);int b=sizeof(int);}")
	language := ObjcLanguage()
	parser := gotreesitter.NewParser(language)
	tree, err := parser.ParseNoResultCompatibilityBenchmarkOnly(source)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(tree.Release)

	root := tree.RootNode()
	custom := findTypeProjectionNode(root, language, source, "sizeof_expression", "sizeof(CustomType)")
	if custom == nil || custom.ChildCount() != 2 {
		t.Fatalf("Objective-C custom sizeof = %v: %s", custom, root.SExpr(language))
	}
	parenthesized := custom.Child(1)
	if parenthesized == nil || parenthesized.Type(language) != "parenthesized_expression" ||
		parenthesized.ChildCount() != 3 ||
		parenthesized.Child(1).Type(language) != "identifier" {
		t.Fatalf("Objective-C custom sizeof operand = %v: %s", parenthesized, root.SExpr(language))
	}
	if got := custom.FieldNameForChild(1, language); got != "value" {
		t.Fatalf("Objective-C custom sizeof field = %q, want value", got)
	}

	primitive := findTypeProjectionNode(root, language, source, "sizeof_expression", "sizeof(int)")
	if primitive == nil || primitive.ChildCount() != 4 ||
		primitive.Child(2).Type(language) != "type_descriptor" {
		t.Fatalf("Objective-C primitive sizeof = %v: %s", primitive, root.SExpr(language))
	}
}
