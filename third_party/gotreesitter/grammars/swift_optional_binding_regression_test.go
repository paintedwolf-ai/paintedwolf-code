package grammars

import (
	"testing"

	"github.com/odvcencio/gotreesitter"
)

func TestSwiftOptionalBindingActionSelection(t *testing.T) {
	lang := SwiftLanguage()
	tests := []struct {
		name           string
		source         string
		wantIf         int
		wantBinding    int
		wantLambda     int
		wantCallSuffix int
	}{
		{name: "if-let", source: "if let x = y {\n}\n", wantIf: 1, wantBinding: 1},
		{name: "if-var", source: "if var x = y {\n}\n", wantIf: 1, wantBinding: 1},
		{name: "trailing-closure", source: "let value = transform(y) { item in item }\n", wantBinding: 1, wantLambda: 1, wantCallSuffix: 2},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			for _, route := range []struct {
				name    string
				compact bool
			}{
				{name: "production"},
				{name: "compact", compact: true},
			} {
				t.Run(route.name, func(t *testing.T) {
					parser := gotreesitter.NewParser(lang)
					parser.SetAdmissionCandidateRoute(route.compact)
					tree, err := parser.Parse([]byte(test.source))
					if err != nil {
						t.Fatalf("parse Swift source: %v", err)
					}
					defer tree.Release()
					root := tree.RootNode()
					if root.HasError() {
						t.Fatalf("Swift source has parse errors: %s", root.SExpr(lang))
					}
					assertSwiftNodeCount(t, lang, root, "if_statement", test.wantIf)
					assertSwiftNodeCount(t, lang, root, "value_binding_pattern", test.wantBinding)
					assertSwiftNodeCount(t, lang, root, "lambda_literal", test.wantLambda)
					assertSwiftNodeCount(t, lang, root, "call_suffix", test.wantCallSuffix)
				})
			}
		})
	}
}

func assertSwiftNodeCount(t *testing.T, lang *gotreesitter.Language, root *gotreesitter.Node, nodeType string, want int) {
	t.Helper()
	if got := countSwiftNodeType(lang, root, nodeType); got != want {
		t.Fatalf("%s count = %d, want %d; tree: %s", nodeType, got, want, root.SExpr(lang))
	}
}
