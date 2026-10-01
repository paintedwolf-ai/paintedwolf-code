//go:build !grammar_subset

package grammars

import (
	"testing"

	gotreesitter "github.com/odvcencio/gotreesitter"
)

const dStorageClassSource = "module m;\nvoid f() { static int value; }"

func TestDStorageClassWrapperNeedsNoResultCompatibility(t *testing.T) {
	language := DLanguage()
	tree, err := gotreesitter.NewParser(language).
		ParseNoResultCompatibilityBenchmarkOnly([]byte(dStorageClassSource))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(tree.Release)

	assertDStorageClassWrapper(t, "compatibility-free", tree, language)
}

func TestDStorageClassWrapperRoutes(t *testing.T) {
	language := DLanguage()
	source := []byte(dStorageClassSource)
	for _, receipt := range retiredDispatchRouteReceiptsAllowCompactFallback(t, language, source) {
		t.Run(receipt.name, func(t *testing.T) {
			assertDStorageClassWrapper(t, receipt.name, receipt.tree, language)
			if receipt.name == "incremental" &&
				(!receipt.incrementalProfile.OldTreeReuseRoute ||
					receipt.incrementalProfile.ReusedSubtrees == 0 ||
					receipt.incrementalProfile.ReusedBytes == 0) {
				t.Fatalf("incremental route did not reuse the old tree: %+v", receipt.incrementalProfile)
			}
		})
	}
}

func assertDStorageClassWrapper(
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
	if declaration == nil || declaration.ChildCount() == 0 {
		t.Fatalf("%s has no variable declaration: %s", route, root.SExpr(language))
	}
	storage := declaration.Child(0)
	if storage == nil || storage.Type(language) != "storage_class" ||
		storage.StartByte() != 21 || storage.EndByte() != 27 ||
		storage.ChildCount() != 1 {
		t.Fatalf("%s storage class = %v, want exact 21..27 unary wrapper: %s", route, storage, declaration.SExpr(language))
	}
	keyword := storage.Child(0)
	if keyword == nil || keyword.Type(language) != "static" ||
		keyword.StartByte() != 21 || keyword.EndByte() != 27 {
		t.Fatalf("%s storage keyword = %v, want static 21..27: %s", route, keyword, storage.SExpr(language))
	}
}
