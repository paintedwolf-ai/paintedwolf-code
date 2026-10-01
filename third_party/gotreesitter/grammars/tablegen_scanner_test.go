//go:build !grammar_subset || grammar_subset_tablegen

package grammars_test

import (
	"testing"

	gts "github.com/odvcencio/gotreesitter"
	"github.com/odvcencio/gotreesitter/grammars"
)

// TestTablegenMultilineCommentInBody guards the external scanner fix that
// matches tree-sitter-tablegen's C scanner: it must skip leading whitespace
// before "/*" (so a block comment with content is recognized when the lexer is
// positioned on the space preceding it, e.g. inside a record body), and it must
// return no token for an unterminated comment. Before the fix, any block
// comment with non-whitespace content inside "{ ... }" fragmented the whole
// parse into an ERROR root, while tree-sitter C parsed it as a comment extra.
func TestTablegenMultilineCommentInBody(t *testing.T) {
	lang := grammars.TablegenLanguage()
	clean := []string{
		`def D { /**/ }`,
		`def D { /*x*/ }`,
		`def D { /* x */ }`,
		`def D { let x = /*c=*/"a"; }`,
		`def D { let x = /*c=*/[{}]; }`,
		`def D { let x = "a" /*c*/; }`,
		`def D { /* outer /* nested */ still */ }`,
		`def D {
  let methods = [
    InterfaceMethod<"Get the declared name",
    "::llvm::StringRef", "getName", (ins),
    /*methodBody=*/[{}],
    /*defaultImplementation=*/[{ return $_op.getName(); }]>,
  ];
}`,
	}
	for _, src := range clean {
		p := gts.NewParser(lang)
		tree, err := p.Parse([]byte(src))
		if err != nil || tree == nil {
			t.Fatalf("parse failed for %q: %v", src, err)
		}
		rn := tree.RootNode()
		if rn.Type(lang) == "ERROR" || rn.HasError() {
			t.Errorf("expected clean parse for %q, got type=%s hasErr=%v", src, rn.Type(lang), rn.HasError())
		}
	}
}
