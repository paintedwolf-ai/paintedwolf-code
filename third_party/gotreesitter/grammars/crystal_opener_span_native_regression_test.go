//go:build !grammar_subset

package grammars

import (
	"strings"
	"testing"

	gotreesitter "github.com/odvcencio/gotreesitter"
)

var crystalBraceOpenerCases = []struct {
	name      string
	source    string
	container string
	wantStart uint32
	wantEnd   uint32
}{
	{
		name:      "inline_hash",
		source:    "x = {1 => 2}\n",
		container: "hash",
		wantStart: 4,
		wantEnd:   5,
	},
	{
		name:      "multiline_hash",
		source:    "x = {\n  1 => 2,\n}\n",
		container: "hash",
		wantStart: 5,
		wantEnd:   5,
	},
	{
		name:      "inline_named_tuple",
		source:    "x = {a: 1}\n",
		container: "named_tuple",
		wantStart: 4,
		wantEnd:   5,
	},
	{
		name:      "multiline_named_tuple",
		source:    "x = {\n  a: 1,\n}\n",
		container: "named_tuple",
		wantStart: 5,
		wantEnd:   5,
	},
}

func TestCrystalBraceOpenersNeedNoResultCompatibility(t *testing.T) {
	for _, test := range crystalBraceOpenerCases {
		t.Run(test.name, func(t *testing.T) {
			source := []byte(test.source)
			language := CrystalLanguage()
			tree, err := gotreesitter.NewParser(language).
				ParseNoResultCompatibilityBenchmarkOnly(source)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(tree.Release)

			root := tree.RootNode()
			if root == nil || root.HasError() {
				t.Fatalf("root = %v, want a clean tree", root)
			}
			assertCrystalBraceOpenerTree(t, "compatibility-free", root, language, test)
		})
	}
}

func TestCrystalBraceOpenerNativeRoutes(t *testing.T) {
	language := CrystalLanguage()
	for _, test := range crystalBraceOpenerCases {
		if test.wantStart == test.wantEnd {
			continue
		}
		t.Run(test.name, func(t *testing.T) {
			baseSource := []byte(strings.TrimSuffix(test.source, "\n"))
			for _, receipt := range retiredDispatchRouteReceipts(t, language, baseSource) {
				assertNoNormalizationPasses(t, receipt.tree)
				assertCrystalBraceOpenerTree(
					t,
					receipt.name,
					receipt.tree.RootNode(),
					language,
					test,
				)
			}
		})
	}
}

func assertCrystalBraceOpenerTree(
	t *testing.T,
	route string,
	root *gotreesitter.Node,
	language *gotreesitter.Language,
	test struct {
		name      string
		source    string
		container string
		wantStart uint32
		wantEnd   uint32
	},
) {
	t.Helper()
	container := findCrystalNamedNode(root, language, test.container)
	if container == nil {
		t.Fatalf("%s missing %s: %s", route, test.container, root.SExpr(language))
	}
	open := container.Child(0)
	if open == nil || open.Type(language) != "{" {
		t.Fatalf("%s %s opener = %v", route, test.container, open)
	}
	if got := open.StartByte(); got != test.wantStart {
		t.Fatalf("%s opener start = %d, want %d", route, got, test.wantStart)
	}
	if got := open.EndByte(); got != test.wantEnd {
		t.Fatalf("%s opener end = %d, want %d", route, got, test.wantEnd)
	}
	if got := container.StartByte(); got != test.wantStart {
		t.Fatalf("%s %s start = %d, want %d", route, test.container, got, test.wantStart)
	}
}

func findCrystalNamedNode(
	node *gotreesitter.Node,
	language *gotreesitter.Language,
	name string,
) *gotreesitter.Node {
	if node == nil {
		return nil
	}
	if node.Type(language) == name {
		return node
	}
	for index := 0; index < node.NamedChildCount(); index++ {
		if found := findCrystalNamedNode(node.NamedChild(index), language, name); found != nil {
			return found
		}
	}
	return nil
}
