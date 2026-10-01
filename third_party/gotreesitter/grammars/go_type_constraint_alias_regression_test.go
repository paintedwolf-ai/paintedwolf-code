package grammars

import (
	"strings"
	"testing"

	"github.com/odvcencio/gotreesitter"
)

// TestGoTypeElemAliasedAsTypeConstraint pins the runtime behavior backing
// go.bin's NonTerminalAliasMap row for type_elem: tree-sitter-go's
// type_parameter_declaration rule aliases $.type_elem to $.type_constraint
// (see tree-sitter-go's grammar.js: `field('type', alias($.type_elem,
// $.type_constraint))`), so a generic type parameter's constraint set
// ("int | string") must display as a type_constraint node, while the SAME
// type_elem production reached unaliased (e.g. a type set inside an
// interface body) must display as plain type_elem.
//
// This is the "real" behavioral half of TestGoNonTerminalAliasMapParity in
// grammargen: that test only checks the derived NonTerminalAliasMap table is
// structurally identical between grammargen and ts2go; this test checks the
// shipped grammars.GoLanguage() blob actually PRODUCES the two distinct
// shapes on live input, i.e. that the alias map is not just structurally
// present but semantically wired up in the parser.
func TestGoTypeElemAliasedAsTypeConstraint(t *testing.T) {
	lang := GoLanguage()

	t.Run("generic-type-parameter-constraint", func(t *testing.T) {
		src := []byte("package main\n\nfunc F[T int | string](x T) {}\n")
		parser := gotreesitter.NewParser(lang)
		tree, err := parser.Parse(src)
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		defer tree.Release()
		root := tree.RootNode()
		if root.HasError() {
			t.Fatalf("generic type parameter constraint produced parse errors: %s", root.SExpr(lang))
		}
		sexpr := root.SExpr(lang)
		if !strings.Contains(sexpr, "(type_constraint") {
			t.Fatalf("expected type_constraint (aliased type_elem) in tree: %s", sexpr)
		}
		if strings.Contains(sexpr, "(type_elem") {
			t.Fatalf("did not expect unaliased type_elem when only the constraint-position occurrence is present: %s", sexpr)
		}
	})

	t.Run("interface-type-set-unaliased", func(t *testing.T) {
		src := []byte("package main\n\ntype Number interface {\n\tint | string\n}\n")
		parser := gotreesitter.NewParser(lang)
		tree, err := parser.Parse(src)
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		defer tree.Release()
		root := tree.RootNode()
		if root.HasError() {
			t.Fatalf("interface type set produced parse errors: %s", root.SExpr(lang))
		}
		sexpr := root.SExpr(lang)
		if !strings.Contains(sexpr, "(type_elem") {
			t.Fatalf("expected unaliased type_elem for an interface type-set: %s", sexpr)
		}
		if strings.Contains(sexpr, "(type_constraint") {
			t.Fatalf("did not expect type_constraint alias outside a type-parameter-declaration constraint position: %s", sexpr)
		}
	})

	t.Run("both-occurrences-in-one-source", func(t *testing.T) {
		// Both shapes present in the same parse: confirms the alias display
		// genuinely varies per-occurrence within a single compiled grammar,
		// not merely across separately-parsed sources.
		src := []byte("package main\n\ntype Number interface {\n\tint | string\n}\n\nfunc F[T int | string](x T) {}\n")
		parser := gotreesitter.NewParser(lang)
		tree, err := parser.Parse(src)
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		defer tree.Release()
		root := tree.RootNode()
		if root.HasError() {
			t.Fatalf("combined source produced parse errors: %s", root.SExpr(lang))
		}
		sexpr := root.SExpr(lang)
		if !strings.Contains(sexpr, "(type_elem") {
			t.Fatalf("expected unaliased type_elem from the interface type-set: %s", sexpr)
		}
		if !strings.Contains(sexpr, "(type_constraint") {
			t.Fatalf("expected type_constraint alias from the generic function's constraint: %s", sexpr)
		}
	})
}
