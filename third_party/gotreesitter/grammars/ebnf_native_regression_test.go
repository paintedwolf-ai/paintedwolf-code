//go:build !grammar_subset

package grammars

import (
	"testing"

	gotreesitter "github.com/odvcencio/gotreesitter"
)

const ebnfRecoveredRootRetirementSource = "rule ::= 'x'"
const ebnfRouteSource = "rule = 'x';"

func TestEBNFRecoveredRootNeedsNoResultCompatibility(t *testing.T) {
	source := []byte(ebnfRecoveredRootRetirementSource)
	language := EbnfLanguage()
	tree, err := gotreesitter.NewParser(language).
		ParseNoResultCompatibilityBenchmarkOnly(source)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(tree.Release)

	assertEBNFRoot(t, "compatibility-free", tree, language, source, true)
}

func TestEBNFRecoveredRootRoutes(t *testing.T) {
	source := []byte(ebnfRouteSource + "\n")
	language := EbnfLanguage()
	for _, receipt := range retiredDispatchRouteReceipts(
		t,
		language,
		[]byte(ebnfRouteSource),
	) {
		assertEBNFRoot(t, receipt.name, receipt.tree, language, source, false)
	}
}

func assertEBNFRoot(
	t *testing.T,
	route string,
	tree *gotreesitter.Tree,
	language *gotreesitter.Language,
	source []byte,
	wantError bool,
) {
	t.Helper()
	root := tree.RootNode()
	if root == nil {
		t.Fatalf("%s returned a nil root", route)
	}
	if got, want := root.Type(language), "syntax"; got != want {
		t.Fatalf("%s root type = %q, want %q: %s", route, got, want, root.SExpr(language))
	}
	if got := root.HasError(); got != wantError {
		t.Fatalf("%s root error = %t, want %t: %s", route, got, wantError, root.SExpr(language))
	}
	if got, want := root.StartByte(), uint32(0); got != want {
		t.Fatalf("%s root start = %d, want %d", route, got, want)
	}
	if got, want := root.EndByte(), uint32(len(source)); got != want {
		t.Fatalf("%s root end = %d, want %d: %s", route, got, want, root.SExpr(language))
	}
}
