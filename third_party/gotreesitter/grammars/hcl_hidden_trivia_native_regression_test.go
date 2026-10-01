//go:build !grammar_subset

package grammars

import (
	"testing"

	gotreesitter "github.com/odvcencio/gotreesitter"
)

func TestHCLHiddenRootTriviaNeedsNoResultCompatibility(t *testing.T) {
	source := hclHiddenRootTriviaSource()
	language := HclLanguage()
	tree, err := gotreesitter.NewParser(language).
		ParseNoResultCompatibilityBenchmarkOnly(source)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(tree.Release)

	assertHCLNativeRootBoundaries(t, "production", tree, language, source)
}

func TestHCLHiddenRootTriviaRoutes(t *testing.T) {
	source := hclHiddenRootTriviaSource()
	routedSource := append(append([]byte(nil), source...), '\n')
	language := HclLanguage()
	for _, receipt := range retiredDispatchRouteReceipts(t, language, source) {
		assertHCLNativeRootBoundaries(t, receipt.name, receipt.tree, language, routedSource)
		if receipt.name != "incremental" {
			continue
		}
		if !receipt.incrementalProfile.ReuseUnsupported ||
			receipt.incrementalProfile.ReuseUnsupportedReason != "external_scanner_unsupported" {
			t.Fatalf("incremental reuse status = %+v", receipt.incrementalProfile)
		}
	}
}

func hclHiddenRootTriviaSource() []byte {
	return []byte("# first\n\n# second\n\nvalue = true")
}

func assertHCLNativeRootBoundaries(
	t *testing.T,
	route string,
	tree *gotreesitter.Tree,
	language *gotreesitter.Language,
	source []byte,
) {
	t.Helper()
	root := tree.RootNode()
	if root == nil || root.HasError() {
		t.Fatalf("%s root = %v, want a clean tree", route, root)
	}
	if got, want := root.Type(language), "config_file"; got != want {
		t.Fatalf("%s root type = %q, want %q", route, got, want)
	}
	if got, want := root.EndByte(), uint32(len(source)); got != want {
		t.Fatalf("%s root end = %d, want %d", route, got, want)
	}
	visibleComments := 0
	for index := 0; index < root.ChildCount(); index++ {
		child := root.Child(index)
		symbol := int(child.Symbol())
		if child.Type(language) == "comment" {
			visibleComments++
		}
		if child.IsExtra() && symbol < len(language.SymbolMetadata) &&
			!language.SymbolMetadata[symbol].Visible {
			t.Fatalf("%s retained hidden root extra: %s", route, root.SExpr(language))
		}
	}
	if visibleComments != 2 {
		t.Fatalf("%s visible comment count = %d, want 2", route, visibleComments)
	}
	bodyCount := 0
	var visit func(*gotreesitter.Node)
	visit = func(node *gotreesitter.Node) {
		if node == nil {
			return
		}
		if node.Type(language) == "body" && node.ChildCount() > 0 {
			bodyCount++
			first := node.Child(0)
			last := node.Child(node.ChildCount() - 1)
			if node.StartByte() != first.StartByte() || node.EndByte() != last.EndByte() {
				t.Fatalf(
					"%s body span = %d:%d, want child span %d:%d",
					route,
					node.StartByte(),
					node.EndByte(),
					first.StartByte(),
					last.EndByte(),
				)
			}
		}
		for index := 0; index < node.ChildCount(); index++ {
			visit(node.Child(index))
		}
	}
	visit(root)
	if bodyCount == 0 {
		t.Fatalf("%s produced no non-empty body", route)
	}
}
