package grammars

import (
	"strings"
	"testing"

	"github.com/odvcencio/gotreesitter"
)

// TestCaddyDirectiveAliasedAsOption pins the runtime behavior backing
// caddy.bin's NonTerminalAliasMap row for directive: tree-sitter-caddy's
// global_options rule aliases $.directive to $.option (grammar.js:
// `repeat1(alias($.directive, $.option))` inside the top-level `{ ... }`
// global options block), so a line inside a global options block must
// display as option, while the SAME directive production reached unaliased
// (a line inside an ordinary server address block) must display as plain
// directive.
//
// This is the "real" behavioral half of TestCaddyNonTerminalAliasMapParity in
// grammargen: that test only checks the derived NonTerminalAliasMap table is
// structurally identical between grammargen and ts2go; this test checks the
// shipped grammars.CaddyLanguage() blob actually PRODUCES the two distinct
// shapes on live input.
func TestCaddyDirectiveAliasedAsOption(t *testing.T) {
	lang := CaddyLanguage()

	t.Run("global-options-block-aliased", func(t *testing.T) {
		src := []byte("{\n\tadmin off\n\tdebug\n}\n")
		parser := gotreesitter.NewParser(lang)
		tree, err := parser.Parse(src)
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		defer tree.Release()
		root := tree.RootNode()
		if root.HasError() {
			t.Fatalf("global options block produced parse errors: %s", root.SExpr(lang))
		}
		sexpr := root.SExpr(lang)
		if !strings.Contains(sexpr, "(option") {
			t.Fatalf("expected option (aliased directive) inside a global_options block: %s", sexpr)
		}
		if strings.Contains(sexpr, "(directive") {
			t.Fatalf("did not expect unaliased directive inside a global_options block: %s", sexpr)
		}
	})

	t.Run("server-block-unaliased", func(t *testing.T) {
		src := []byte("example.com {\n\treverse_proxy localhost:9000\n}\n")
		parser := gotreesitter.NewParser(lang)
		tree, err := parser.Parse(src)
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		defer tree.Release()
		root := tree.RootNode()
		if root.HasError() {
			t.Fatalf("server block produced parse errors: %s", root.SExpr(lang))
		}
		sexpr := root.SExpr(lang)
		if !strings.Contains(sexpr, "(directive") {
			t.Fatalf("expected unaliased directive inside a server address block: %s", sexpr)
		}
		if strings.Contains(sexpr, "(option") {
			t.Fatalf("did not expect option alias outside a global_options block: %s", sexpr)
		}
	})
}
