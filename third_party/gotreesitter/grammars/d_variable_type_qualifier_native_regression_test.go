//go:build !grammar_subset

package grammars

import (
	"testing"

	gotreesitter "github.com/odvcencio/gotreesitter"
)

const dVariableTypeQualifierSource = "static shared Mallocator instance;"

func TestDVariableTypeQualifierNeedsNoResultCompatibility(t *testing.T) {
	language := DLanguage()
	source := []byte(dVariableTypeQualifierSource)
	tree, err := gotreesitter.NewParser(language).
		ParseNoResultCompatibilityBenchmarkOnly(source)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(tree.Release)

	assertDVariableTypeQualifier(t, "compatibility-free", tree, language)
}

func TestDVariableTypeQualifierRoutes(t *testing.T) {
	language := DLanguage()
	source := []byte(dVariableTypeQualifierSource)
	for _, receipt := range retiredDispatchRouteReceiptsAllowCompactFallback(t, language, source) {
		t.Run(receipt.name, func(t *testing.T) {
			assertDVariableTypeQualifier(t, receipt.name, receipt.tree, language)
			if receipt.name == "incremental" &&
				(!receipt.incrementalProfile.OldTreeReuseRoute ||
					receipt.incrementalProfile.ReusedSubtrees == 0 ||
					receipt.incrementalProfile.ReusedBytes == 0) {
				t.Fatalf("incremental route did not reuse the old tree: %+v", receipt.incrementalProfile)
			}
		})
	}
}

func assertDVariableTypeQualifier(
	t *testing.T,
	route string,
	tree *gotreesitter.Tree,
	language *gotreesitter.Language,
) {
	t.Helper()
	root := tree.RootNode()
	if root == nil || root.HasError() {
		t.Fatalf("%s root = %v, want a clean tree", route, root)
	}
	declaration := findFirstNamedDescendantWhere(
		root,
		language,
		"variable_declaration",
		func(*gotreesitter.Node) bool { return true },
	)
	if declaration == nil || declaration.ChildCount() != 4 {
		t.Fatalf("%s variable declaration = %v, want four children: %s", route, declaration, root.SExpr(language))
	}
	storage := declaration.Child(0)
	if storage == nil || storage.Type(language) != "storage_class" ||
		storage.StartByte() != 0 || storage.EndByte() != 6 {
		t.Fatalf("%s storage class = %v, want static at 0..6: %s", route, storage, declaration.SExpr(language))
	}
	typ := declaration.Child(1)
	if typ == nil || typ.Type(language) != "type" ||
		typ.StartByte() != 7 || typ.EndByte() != 24 ||
		typ.ChildCount() != 2 {
		t.Fatalf("%s type = %v, want qualified type at 7..24: %s", route, typ, declaration.SExpr(language))
	}
	qualifier := typ.Child(0)
	if qualifier == nil || qualifier.Type(language) != "type_ctor" ||
		qualifier.StartByte() != 7 || qualifier.EndByte() != 13 {
		t.Fatalf("%s qualifier = %v, want shared type constructor at 7..13: %s", route, qualifier, typ.SExpr(language))
	}
	name := typ.Child(1)
	if name == nil || name.Type(language) != "identifier" ||
		name.StartByte() != 14 || name.EndByte() != 24 {
		t.Fatalf("%s type name = %v, want Mallocator identifier at 14..24: %s", route, name, typ.SExpr(language))
	}
}
