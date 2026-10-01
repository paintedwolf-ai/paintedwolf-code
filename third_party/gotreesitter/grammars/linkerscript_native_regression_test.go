//go:build !grammar_subset

package grammars

import (
	"os"
	"path/filepath"
	"testing"

	gotreesitter "github.com/odvcencio/gotreesitter"
)

func TestLinkerscriptNeedsNoResultCompatibility(t *testing.T) {
	language := LinkerscriptLanguage()
	for _, test := range []struct {
		name      string
		wantError bool
	}{
		{name: "clean.lds"},
		{name: "recovered.lds", wantError: true},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			source := linkerscriptRetirementFixture(t, test.name)
			tree, err := gotreesitter.NewParser(language).
				ParseNoResultCompatibilityBenchmarkOnly(source)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(tree.Release)

			assertLinkerscriptNativeResult(t, tree, language, source, test.wantError)
			assertNoNormalizationPasses(t, tree)
		})
	}
}

func TestLinkerscriptRetiredDispatchArmRoutes(t *testing.T) {
	language := LinkerscriptLanguage()
	baseSource := []byte("SECTIONS { .text : { *(.text) } }")
	source := append(append([]byte(nil), baseSource...), '\n')
	for _, receipt := range retiredDispatchRouteReceipts(t, language, baseSource) {
		assertLinkerscriptNativeResult(t, receipt.tree, language, source, false)
	}
}

func linkerscriptRetirementFixture(t *testing.T, name string) []byte {
	t.Helper()
	source, err := os.ReadFile(
		filepath.Join("..", "testdata", "linkerscript_retirement", name),
	)
	if err != nil {
		t.Fatal(err)
	}
	return source
}

func assertLinkerscriptNativeResult(
	t *testing.T,
	tree *gotreesitter.Tree,
	language *gotreesitter.Language,
	source []byte,
	wantError bool,
) {
	t.Helper()
	root := tree.RootNode()
	if root == nil {
		t.Fatal("nil root")
	}
	if got, want := root.Type(language), "linkerscript"; got != want {
		t.Fatalf("root type = %q, want %q: %s", got, want, root.SExpr(language))
	}
	if got := root.HasError(); got != wantError {
		t.Fatalf("root error = %t, want %t: %s", got, wantError, root.SExpr(language))
	}
	if got, want := root.EndByte(), uint32(len(source)); got != want {
		t.Fatalf("root end = %d, want %d: %s", got, want, root.SExpr(language))
	}

	errorCount := 0
	var visit func(*gotreesitter.Node)
	visit = func(node *gotreesitter.Node) {
		if node == nil {
			return
		}
		if node.IsError() {
			errorCount++
			if !node.IsNamed() {
				t.Errorf("unnamed ERROR node: %s", root.SExpr(language))
			}
		}
		for index := 0; index < node.ChildCount(); index++ {
			visit(node.Child(index))
		}
	}
	visit(root)
	if wantError && errorCount == 0 {
		t.Fatalf("fixture produced no ERROR node: %s", root.SExpr(language))
	}
}

func assertNoNormalizationPasses(t *testing.T, tree *gotreesitter.Tree) {
	t.Helper()
	runtime := tree.ParseRuntime()
	if runtime.NormalizationPassesChecked != 0 ||
		runtime.NormalizationPassesRun != 0 ||
		runtime.NormalizationNodesVisited != 0 ||
		runtime.NormalizationNodesRewritten != 0 {
		t.Fatalf(
			"normalization checked/run/visited/rewritten=%d/%d/%d/%d",
			runtime.NormalizationPassesChecked,
			runtime.NormalizationPassesRun,
			runtime.NormalizationNodesVisited,
			runtime.NormalizationNodesRewritten,
		)
	}
}
