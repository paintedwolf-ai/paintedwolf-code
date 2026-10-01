//go:build !grammar_subset

package grammars

import (
	"testing"

	gotreesitter "github.com/odvcencio/gotreesitter"
)

const dModuleSpanRetirementSource = "// license\n\nmodule test;\nimport std.stdio;"

func TestDModuleSpanNeedsNoResultCompatibility(t *testing.T) {
	source := []byte(dModuleSpanRetirementSource)
	language := DLanguage()
	tree, err := gotreesitter.NewParser(language).
		ParseNoResultCompatibilityBenchmarkOnly(source)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(tree.Release)

	assertDModuleSpan(t, "compatibility-free", tree, language, source)
}

func TestDModuleSpanRoutes(t *testing.T) {
	source := []byte(dModuleSpanRetirementSource + "\n")
	language := DLanguage()
	for _, receipt := range retiredDispatchRouteReceipts(
		t,
		language,
		[]byte(dModuleSpanRetirementSource),
	) {
		assertDModuleSpan(t, receipt.name, receipt.tree, language, source)
		if receipt.name != "incremental" {
			continue
		}
		profile := receipt.incrementalProfile
		if !profile.OldTreeReuseRoute || profile.ReusedSubtrees == 0 ||
			profile.ReusedBytes == 0 {
			t.Fatalf("incremental route did not reuse the old tree: %+v", profile)
		}
	}
}

func assertDModuleSpan(
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
	var moduleDef *gotreesitter.Node
	for index := 0; index < root.NamedChildCount(); index++ {
		child := root.NamedChild(index)
		if child != nil && child.Type(language) == "module_def" {
			moduleDef = child
			break
		}
	}
	if moduleDef == nil {
		t.Fatalf("%s has no module_def: %s", route, root.SExpr(language))
	}
	if got, want := moduleDef.StartByte(), uint32(12); got != want {
		t.Fatalf("%s module_def start = %d, want %d", route, got, want)
	}
	wantEnd := len(source)
	if wantEnd > 0 && source[wantEnd-1] == '\n' {
		wantEnd--
	}
	if got, want := moduleDef.EndByte(), uint32(wantEnd); got != want {
		t.Fatalf("%s module_def end = %d, want %d", route, got, want)
	}
	if got, want := moduleDef.StartPoint(), (gotreesitter.Point{Row: 2}); got != want {
		t.Fatalf("%s module_def start point = %#v, want %#v", route, got, want)
	}
	if got, want := moduleDef.EndPoint(), (gotreesitter.Point{Row: 3, Column: 17}); got != want {
		t.Fatalf("%s module_def end point = %#v, want %#v", route, got, want)
	}
}
