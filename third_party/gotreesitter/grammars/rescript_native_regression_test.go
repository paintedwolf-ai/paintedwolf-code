//go:build !grammar_subset

package grammars

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	gotreesitter "github.com/odvcencio/gotreesitter"
)

func TestRescriptNeedsNoResultCompatibility(t *testing.T) {
	language := RescriptLanguage()
	for _, test := range []struct {
		name string
		want string
	}{
		{
			name: "alias.res",
			want: "(source_file (expression_statement (value_identifier_path (module_identifier) (value_identifier))))",
		},
		{
			name: "control.res",
			want: "(source_file (let_declaration (let_binding (value_identifier) (number))))",
		},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			source := rescriptRetirementFixture(t, test.name)
			for _, parse := range []struct {
				name string
				run  func([]byte) (*gotreesitter.Tree, error)
			}{
				{
					name: "native",
					run: gotreesitter.NewParser(language).
						ParseNoResultCompatibilityBenchmarkOnly,
				},
				{
					name: "production",
					run:  gotreesitter.NewParser(language).Parse,
				},
			} {
				tree, err := parse.run(source)
				if err != nil {
					t.Fatalf("%s parse: %v", parse.name, err)
				}
				t.Cleanup(tree.Release)
				root := tree.RootNode()
				if root == nil || root.HasError() {
					t.Fatalf("%s invalid root", parse.name)
				}
				if got := root.SExpr(language); got != test.want {
					t.Fatalf("%s shape = %s, want %s", parse.name, got, test.want)
				}
				assertNoNormalizationPasses(t, tree)
			}
		})
	}
}

func TestRescriptRetiredDispatchArmRoutes(t *testing.T) {
	language := RescriptLanguage()
	source := bytes.TrimSuffix(
		rescriptRetirementFixture(t, "alias.res"),
		[]byte{'\n'},
	)
	const want = "(source_file (expression_statement (value_identifier_path (module_identifier) (value_identifier))))"
	for _, receipt := range retiredDispatchRouteReceiptsAllowCompactFallback(
		t,
		language,
		source,
	) {
		root := receipt.tree.RootNode()
		if root == nil || root.HasError() {
			t.Fatalf("%s invalid root", receipt.name)
		}
		if got := root.SExpr(language); got != want {
			t.Fatalf("%s shape = %s, want %s", receipt.name, got, want)
		}
		assertNoNormalizationPasses(t, receipt.tree)
	}
}

func rescriptRetirementFixture(t *testing.T, name string) []byte {
	t.Helper()
	source, err := os.ReadFile(
		filepath.Join("..", "testdata", "rescript_retirement", name),
	)
	if err != nil {
		t.Fatal(err)
	}
	return source
}
