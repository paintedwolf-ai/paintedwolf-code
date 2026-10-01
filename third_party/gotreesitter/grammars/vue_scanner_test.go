//go:build !grammar_subset || grammar_subset_vue

package grammars

import (
	"testing"

	gotreesitter "github.com/odvcencio/gotreesitter"
)

// TestVueCommentAfterNewlineInTemplate guards against a scanner regression where
// an HTML comment preceded by a newline (whitespace-only text run) inside a
// <template> dead-ended the parse, fragmenting the whole document into an ERROR
// root. The C scanner's inline text-fragment block falls through to comment
// scanning when the run is whitespace-only and the next char is `<`; the Go port
// must preserve that fall-through. See vue_scanner.go vueScanTextFragment.
func TestVueCommentAfterNewlineInTemplate(t *testing.T) {
	lang := VueLanguage()

	cases := []struct {
		name string
		src  string
	}{
		{"newline+spaces before comment", "<template>\n  <!-- comment -->\n</template>\n"},
		{"newline before comment", "<template>\n<!-- c --></template>\n"},
		{"newline+space before comment", "<template>\n <!-- c --></template>\n"},
		{"comment in div after newline", "<div>\n  <!-- c -->\n</div>\n"},
		// Cases that already worked - keep them green.
		{"space before comment", "<template> <!-- c --></template>\n"},
		{"comment immediately after tag", "<template><!-- c --></template>\n"},
		{"comment after text", "<template>x<!-- c --></template>\n"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := gotreesitter.NewParser(lang)
			tree, err := p.Parse([]byte(tc.src))
			if err != nil {
				t.Fatalf("Parse error: %v", err)
			}
			root := tree.RootNode()
			if root.Type(lang) == "ERROR" {
				t.Fatalf("root type = ERROR, want document; src=%q", tc.src)
			}
			if root.HasError() {
				t.Fatalf("tree has error, want clean parse; src=%q", tc.src)
			}
		})
	}
}
